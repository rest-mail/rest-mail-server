package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// clientKind is how a filter is handed something a mail client is given.
type clientKind int

const (
	// A text a client reads: it is in a leaf of the body tree the filters are handed.
	readPart clientKind = iota
	// An attachment a client saves: it is a filter attachment with that name and those octets, and
	// the REST client lists and downloads it.
	savedPart
	// An attachment inside an encapsulated message that isn't itself an attachment. Filters are
	// handed the encapsulated message as one message/rfc822 body part holding its source, so this is
	// in that source, encoded as it was sent, and is not a filter attachment of its own; the REST
	// client doesn't list it either. Documented difference (#1968, #2107 MIME AC03).
	encapsulatedPart
	// A part with no name and no disposition that isn't text. Filters are handed it as a body part
	// holding its octets; filters that receive the message as JSON (webhook, JavaScript sidecar) get
	// those octets as a JSON string, so an octet that isn't UTF-8 reaches them as U+FFFD. The REST
	// client doesn't list it. Documented difference (#1968, #2107 MIME AC03).
	unnamedBinaryPart
)

// clientPart is one thing a mail client is given of a message, at the IMAP section it fetches it from.
type clientPart struct {
	kind    clientKind
	section string
	// wire is what BODY[section] holds, in the transfer encoding it was sent in.
	wire string
	// text is what a client reads there once it has undone the transfer encoding and charset.
	text string
	// filename and octets are an attachment as a client saves it.
	filename string
	octets   []byte
}

func readText(section, wire, text string) clientPart {
	return clientPart{kind: readPart, section: section, wire: wire, text: text}
}

func savedFile(section, filename string, octets []byte) clientPart {
	return clientPart{kind: savedPart, section: section, wire: base64.StdEncoding.EncodeToString(octets), filename: filename, octets: octets}
}

// TestFiltersAndScannersSeeWhatAClientReceives is #1968's "filters and scanners cannot miss content a
// client receives" (#2107 MIME AC03), on the production route. Each message is sent to the inbound
// SMTP gateway, which reads the subject and sender name and hands the source to the router's delivery
// route. Its inbound pipeline is a filter that writes down what every filter is handed, the real
// clamav and rspamd filters (their HTTP services stubbed, so what they are sent is kept), and
// extract_attachments into the server's attachment store. Then a mail client reads it: the IMAP
// gateway's BODY[section] for every text and attachment, and the REST message, attachment list and
// download.
//
// Every text a client reads has to be in a body part the filters are handed, after a JSON round trip
// too (webhook and JavaScript filters), and in what both scanners were sent. Every attachment a client
// saves has to be a filter attachment with that name and those octets, listed and downloaded alike by
// the REST client, and in what both scanners were sent. The subject and sender name the REST client
// shows have to be the ones the filters read. The rows marked as documented differences pin what
// happens today, which the report on #2107 lists.
func TestFiltersAndScannersSeeWhatAClientReceives(t *testing.T) {
	store := s3test.New(t)
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize, Attachments: store.Config})
	var domain models.Domain
	if err := f.db.First(&domain, "name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	scanner := newStubScanner()
	scanners := httptest.NewServer(scanner.handler)
	t.Cleanup(scanners.Close)
	seen := &seenByFilters{}
	pipeline.DefaultRegistry.Register("seen_by_filters", func([]byte) (pipeline.Filter, error) { return seen, nil })
	filters := fmt.Sprintf(`[{"name":"seen_by_filters","type":"action","enabled":true},`+
		`{"name":"clamav","type":"action","enabled":true,"config":{"url":%q,"fallback_action":"reject"}},`+
		`{"name":"rspamd","type":"action","enabled":true,"config":{"url":%q,"fallback_action":"reject"}},`+
		`{"name":"extract_attachments","type":"transform","enabled":true}]`, scanners.URL, scanners.URL)
	if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(filters), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	imap := openIMAPSession(t, f, f.gatewayClient(srv.URL, imapCN))
	inbound, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gateway := smtpgw.NewServer("mail.example.test", apiclient.New(srv.URL), nil,
		smtpgw.NewStore(f.db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 100}))
	if err := gateway.Serve(smtpgw.SMTPListeners{Inbound: inbound}); err != nil {
		t.Fatalf("start the SMTP gateway: %v", err)
	}
	t.Cleanup(gateway.Shutdown)

	const head = "From: sender@remote.test\r\nTo: chris@example.test\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nMIME-Version: 1.0\r\n"
	b64 := func(octets []byte) string { return base64.StdEncoding.EncodeToString(octets) }
	exe := []byte("MZ\x90\x00\x03\x00\x00\x00\x04\x00\xff\xff evil")
	pdf := []byte("%PDF-1.7\n\x00\x01\x02\xfe\xff body")
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	latin2 := []byte("Za\xbf\xf3\xb3\xe6 g\xea\xb6l\xb1 ja\xbc\xf1")

	for _, row := range []struct {
		name, message string
		// subject and fromName are what the REST message shows and the filters must have read.
		subject, fromName string
		// envelope, when set, is in the ENVELOPE an IMAP client is sent.
		envelope string
		parts    []clientPart
	}{
		{
			name: "encoded words in many charsets",
			message: "From: =?iso-8859-2?Q?=A3=F3d=BC?= <sender@remote.test>\r\nTo: chris@example.test\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n" +
				"Subject: =?koi8-r?B?8NLJ18XU?= =?shift_jis?B?k/qWe4zq?= =?gb18030?B?1tDOxA==?= =?iso-8859-1?Q?caf=E9?=\r\nMIME-Version: 1.0\r\n" +
				"Content-Type: text/plain; charset=utf-8\r\n\r\nplain words\r\n",
			subject: "Привет日本語中文café", fromName: "Łódź",
			parts: []clientPart{readText("1", "plain words", "plain words")},
		},
		{
			name: "folded encoded words in a folded subject",
			message: "From: \"Folded\r\n Name\" <sender@remote.test>\r\nTo: chris@example.test\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n" +
				"Subject: =?utf-8?Q?first_half?=\r\n =?utf-8?Q?_second_half?=\r\n\tand text\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nbody\r\n",
			subject: "first half second half and text", fromName: "Folded Name",
			parts: []clientPart{readText("1", "body", "body")},
		},
		{
			name: "raw UTF-8 in headers",
			message: "From: J\xc3\xb6rg M\xc3\xbcller <sender@remote.test>\r\nTo: chris@example.test\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n" +
				"Subject: gr\xc3\xbc\xc3\x9fe \xe2\x82\xac\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\nk\xc3\xa4se\r\n",
			subject: "grüße €", fromName: "Jörg Müller",
			parts: []clientPart{readText("1", "k\xc3\xa4se", "käse")},
		},
		{
			name: "duplicate Subject and From fields",
			message: "From: First <first@remote.test>\r\nFrom: Second <second@remote.test>\r\nTo: chris@example.test\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n" +
				"Subject: first subject\r\nSubject: second subject\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nbody\r\n",
			subject: "first subject", fromName: "First",
			envelope: `"first subject" (("First" NIL "first" "remote.test"))`,
			parts:    []clientPart{readText("1", "body", "body")},
		},
		{
			name: "a base64 body in Latin-2",
			message: head + "Subject: s\r\nContent-Type: text/plain; charset=iso-8859-2\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
				b64(latin2) + "\r\n",
			subject: "s",
			parts:   []clientPart{readText("1", b64(latin2), "Zażółć gęślą jaźń")},
		},
		{
			name: "a quoted-printable HTML body in Windows-1252",
			message: head + "Subject: s\r\nContent-Type: text/html; charset=windows-1252\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
				"<p>caf=E9 =80 soft=\r\n break</p>\r\n",
			subject: "s",
			parts:   []clientPart{readText("1", "caf=E9 =80 soft=", "<p>café € soft break</p>")},
		},
		{
			name: "text and HTML alternatives beside an attachment",
			message: head + "Subject: s\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n" +
				"--m\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n" +
				"--a\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nthe plain alternative =E2=82=AC\r\n" +
				"--a\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64([]byte("<b>the html alternative</b>")) + "\r\n--a--\r\n" +
				"--m\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename*=iso-8859-1''r%E9sum%E9.pdf\r\n\r\n" +
				b64(pdf) + "\r\n--m--\r\n",
			subject: "s",
			parts: []clientPart{
				readText("1.1", "the plain alternative =E2=82=AC", "the plain alternative €"),
				readText("1.2", b64([]byte("<b>the html alternative</b>")), "<b>the html alternative</b>"),
				savedFile("2", "résumé.pdf", pdf),
			},
		},
		{
			name: "an alternative no client shows first",
			message: head + "Subject: s\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n" +
				"--a\r\nContent-Type: text/plain\r\n\r\nshown text\r\n" +
				"--a\r\nContent-Type: text/x-hidden\r\n\r\nhidden alternative text\r\n" +
				"--a\r\nContent-Type: text/html\r\n\r\n<p>shown html</p>\r\n--a--\r\n",
			subject: "s",
			parts: []clientPart{
				readText("1", "shown text", "shown text"),
				readText("2", "hidden alternative text", "hidden alternative text"),
				readText("3", "<p>shown html</p>", "<p>shown html</p>"),
			},
		},
		{
			name: "two texts in a mixed body, with no attachment",
			message: head + "Subject: s\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n" +
				"--m\r\nContent-Type: text/plain\r\n\r\nfirst text\r\n--m\r\nContent-Type: text/plain\r\n\r\nsecond text\r\n--m--\r\n",
			subject: "s",
			parts:   []clientPart{readText("1", "first text", "first text"), readText("2", "second text", "second text")},
		},
		{
			name: "related HTML with an inline image, nested in mixed",
			message: head + "Subject: s\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n" +
				"--m\r\nContent-Type: multipart/related; boundary=r\r\n\r\n" +
				"--r\r\nContent-Type: multipart/alternative; boundary=a\r\n\r\n" +
				"--a\r\nContent-Type: text/plain\r\n\r\ndeep plain\r\n--a\r\nContent-Type: text/html\r\n\r\n<img src=\"cid:i@x\">deep html\r\n--a--\r\n" +
				"--r\r\nContent-Type: image/png\r\nContent-ID: <i@x>\r\nContent-Disposition: inline; filename=pic.png\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
				b64(png) + "\r\n--r--\r\n" +
				"--m\r\nContent-Type: application/octet-stream; name=\"=?utf-8?B?" + b64([]byte("файл.exe")) + "?=\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
				b64(exe) + "\r\n--m--\r\n",
			subject: "s",
			parts: []clientPart{
				readText("1.1.1", "deep plain", "deep plain"),
				readText("1.1.2", "deep html", "deep html"),
				savedFile("1.2", "pic.png", png),
				savedFile("2", "файл.exe", exe),
			},
		},
		{
			name: "RFC 2231 continued filename in Windows-1252",
			message: head + "Subject: s\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
				"--m\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n" +
				"Content-Disposition: attachment;\r\n filename*0*=windows-1252''J%F6rg%80;\r\n filename*1=\"-part\";\r\n filename*2*=.exe\r\n\r\n" +
				b64(exe) + "\r\n--m--\r\n",
			subject: "s",
			parts:   []clientPart{readText("1", "see attached", "see attached"), savedFile("2", "Jörg€-part.exe", exe)},
		},
		{
			name: "a forwarded message, attached",
			message: head + "Subject: s\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\nforwarding\r\n" +
				"--m\r\nContent-Type: message/rfc822\r\nContent-Disposition: attachment; filename=fwd.eml\r\n\r\n" +
				"Subject: inner\r\nContent-Type: text/plain\r\n\r\ninner forwarded words\r\n--m--\r\n",
			subject: "s",
			parts: []clientPart{
				readText("1", "forwarding", "forwarding"),
				{kind: savedPart, section: "2", wire: "inner forwarded words", filename: "fwd.eml", octets: []byte("Subject: inner\r\nContent-Type: text/plain\r\n\r\ninner forwarded words")},
			},
		},
		// An encoded NUL: the filters have the NUL a client decodes. Storage can't hold a NUL, so the
		// REST text has U+FFFD there, and the subject's NUL reads as a space everywhere (#1921).
		{
			name:    "an encoded NUL in the subject and the body",
			message: head + "Subject: =?utf-8?Q?a=00b?=\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nbe=00fore\r\n",
			subject: "a b",
			parts:   []clientPart{readText("1", "be=00fore", "be\x00fore")},
		},
		// UTF-7 is decided on #1968 and not here: nothing on this server decodes it, so the filters,
		// the scanners and the REST client all hold its own spelling, while a client that decodes
		// UTF-7 reads U+0000 in both. Pinned as it is until that decision.
		{
			name:    "UTF-7 that spells a NUL, undecided",
			message: head + "Subject: =?utf-7?Q?a+AAA-b?=\r\nContent-Type: text/plain; charset=utf-7\r\n\r\nHi +AAA- Mom\r\n",
			subject: "=?utf-7?Q?a+AAA-b?=",
			parts:   []clientPart{readText("1", "Hi +AAA- Mom", "Hi +AAA- Mom")},
		},
		{
			name: "a forwarded message that isn't an attachment, holding one, documented",
			message: head + "Subject: s\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\nforwarding\r\n" +
				"--m\r\nContent-Type: message/rfc822\r\n\r\n" +
				"Subject: inner\r\nContent-Type: multipart/mixed; boundary=i\r\n\r\n--i\r\nContent-Type: text/plain\r\n\r\ninner words\r\n" +
				"--i\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=inner.exe\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
				b64(exe) + "\r\n--i--\r\n--m--\r\n",
			subject: "s",
			parts: []clientPart{
				readText("1", "forwarding", "forwarding"),
				readText("2.1", "inner words", "inner words"),
				{kind: encapsulatedPart, section: "2.2", wire: b64(exe), filename: "inner.exe", octets: exe},
			},
		},
		{
			name: "a binary part with no name, documented",
			message: head + "Subject: s\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n--m\r\nContent-Type: text/plain\r\n\r\nsee below\r\n" +
				"--m\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\n\r\n" + b64(pdf) + "\r\n--m--\r\n",
			subject: "s",
			parts: []clientPart{
				readText("1", "see below", "see below"),
				{kind: unnamedBinaryPart, section: "2", wire: b64(pdf), octets: pdf},
			},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := f.db.Where("mailbox_id = ?", f.mailboxID).Delete(&models.Message{}).Error; err != nil {
				t.Fatal(err)
			}
			scanner.mu.Lock()
			scanner.clamav, scanner.rspamd = "", ""
			scanner.mu.Unlock()
			seen.mu.Lock()
			seen.email = nil
			seen.mu.Unlock()
			client, err := smtp.Dial(inbound.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			if err := client.Hello("mail.remote.test"); err != nil {
				t.Fatal(err)
			}
			if err := send(client, "sender@remote.test", f.address, row.message); err != nil {
				t.Fatalf("send: %v", err)
			}
			stored := lastStored(t, f, row.message)

			seen.mu.Lock()
			email := seen.email
			seen.mu.Unlock()
			if email == nil {
				t.Fatal("no filter ran")
			}
			// What a webhook or the JavaScript sidecar is sent is this, as JSON.
			encoded, err := json.Marshal(email)
			if err != nil {
				t.Fatal(err)
			}
			var asJSON pipeline.EmailJSON
			if err := json.Unmarshal(encoded, &asJSON); err != nil {
				t.Fatal(err)
			}
			scanner.mu.Lock()
			sent := map[string]string{"clamav": scanner.clamav, "rspamd": scanner.rspamd}
			scanner.mu.Unlock()
			texts, jsonTexts := filterTexts(email.Body), filterTexts(asJSON.Body)

			// What the REST client is shown.
			var message struct {
				Data struct {
					Subject    string `json:"subject"`
					SenderName string `json:"sender_name"`
					BodyText   string `json:"body_text"`
					BodyHTML   string `json:"body_html"`
				} `json:"data"`
			}
			answer := f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", stored.ID), f.session, "")
			if answer.Code != http.StatusOK || json.Unmarshal(answer.Body.Bytes(), &message) != nil {
				t.Fatalf("GET message: %d %.300s", answer.Code, answer.Body)
			}
			if message.Data.Subject != row.subject || email.Headers.Subject != row.subject || asJSON.Headers.Subject != row.subject {
				t.Errorf("subject: client shown %q, filters read %q, as JSON %q, want %q", message.Data.Subject, email.Headers.Subject, asJSON.Headers.Subject, row.subject)
			}
			filterFrom := ""
			if from, ok := email.Headers.From.First(); ok {
				filterFrom = from.Name
			}
			if message.Data.SenderName != row.fromName || filterFrom != row.fromName {
				t.Errorf("sender name: client shown %q, filters read %q, want %q", message.Data.SenderName, filterFrom, row.fromName)
			}
			// Storage holds no NUL, and has U+FFFD where the filters have one.
			storedTexts := make([]string, len(texts))
			for i, text := range texts {
				storedTexts[i] = strings.ReplaceAll(text, "\x00", "\uFFFD")
			}
			for name, text := range map[string]string{"body_text": message.Data.BodyText, "body_html": message.Data.BodyHTML} {
				if text = strings.TrimSpace(text); text != "" && !containsAny(storedTexts, text) {
					t.Errorf("REST %s %q is in no body part the filters were handed: %q", name, text, texts)
				}
			}
			var listed struct {
				Data []struct {
					ID       uint   `json:"id"`
					Filename string `json:"filename"`
				} `json:"data"`
			}
			answer = f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/attachments", stored.ID), f.session, "")
			if answer.Code != http.StatusOK || json.Unmarshal(answer.Body.Bytes(), &listed) != nil {
				t.Fatalf("GET attachments: %d %.300s", answer.Code, answer.Body)
			}
			downloads := map[string][]byte{}
			for _, a := range listed.Data {
				got := f.do(http.MethodGet, fmt.Sprintf("/api/v1/attachments/%d", a.ID), f.session, "")
				if got.Code != http.StatusOK {
					t.Fatalf("download %q: %d %.300s", a.Filename, got.Code, got.Body)
				}
				downloads[a.Filename] = got.Body.Bytes()
			}
			// Everything the REST client can save is a filter attachment with the same octets.
			for name, octets := range downloads {
				if got, ok := filterAttachment(email, name); !ok || !bytes.Equal(got, octets) {
					t.Errorf("REST attachment %q (%q) is not a filter attachment with those octets: %v %q", name, octets, ok, got)
				}
			}

			imap.do("SELECT INBOX")
			if row.envelope != "" {
				if got := imap.do("FETCH 1 (ENVELOPE)"); !strings.Contains(got, row.envelope) {
					t.Errorf("ENVELOPE %s, want %s in it", got, row.envelope)
				}
			}
			for _, part := range row.parts {
				fetched := imap.do("FETCH 1 (BODY.PEEK[" + part.section + "])")
				if !strings.Contains(unfolded(fetched), unfolded(part.wire)) {
					t.Errorf("BODY[%s] %.400q, want %q in it", part.section, fetched, part.wire)
				}
				switch part.kind {
				case readPart:
					if !containsAny(texts, part.text) || !containsAny(jsonTexts, part.text) {
						t.Errorf("text %q in BODY[%s] is in no body part the filters were handed: %q, as JSON %q", part.text, part.section, texts, jsonTexts)
					}
					for name, body := range sent {
						if !strings.Contains(body, part.text) && !strings.Contains(unfolded(body), unfolded(part.wire)) {
							t.Errorf("%s was sent %.600q, which doesn't hold BODY[%s]'s text %q", name, body, part.section, part.text)
						}
					}
				case savedPart:
					if octets, ok := filterAttachment(email, part.filename); !ok || !bytes.Equal(octets, part.octets) {
						t.Errorf("attachment %q in BODY[%s] is not a filter attachment with its octets: %v %q", part.filename, part.section, ok, octets)
					}
					if octets, ok := filterAttachment(&asJSON, part.filename); !ok || !bytes.Equal(octets, part.octets) {
						t.Errorf("attachment %q in BODY[%s] is not a JSON filter attachment with its octets: %v %q", part.filename, part.section, ok, octets)
					}
					if got, ok := downloads[part.filename]; !ok || !bytes.Equal(got, part.octets) {
						t.Errorf("REST download of %q: listed %v, %q, want %q", part.filename, ok, got, part.octets)
					}
					for name, body := range sent {
						if !strings.Contains(unfolded(body), b64(part.octets)) {
							t.Errorf("%s was sent %.600q, which doesn't hold attachment %q", name, body, part.filename)
						}
					}
				case encapsulatedPart:
					if _, ok := filterAttachment(email, part.filename); ok {
						t.Errorf("attachment %q in BODY[%s] is now a filter attachment: update this row and #2107's report", part.filename, part.section)
					}
					if _, ok := downloads[part.filename]; ok {
						t.Errorf("attachment %q in BODY[%s] is now listed by REST: update this row and #2107's report", part.filename, part.section)
					}
					if !containsAny(texts, "filename="+part.filename) || !containsAny(texts, part.wire) || !containsAny(jsonTexts, part.wire) {
						t.Errorf("attachment %q in BODY[%s] is in no message/rfc822 body part's source the filters were handed: %q", part.filename, part.section, texts)
					}
					for name, body := range sent {
						if !strings.Contains(unfolded(body), part.wire) {
							t.Errorf("%s was sent %.600q, which doesn't hold %q", name, body, part.filename)
						}
					}
				case unnamedBinaryPart:
					if len(downloads) != 0 || len(email.Attachments) != 0 {
						t.Errorf("an unnamed part is now an attachment (REST %d, filters %d): update this row and #2107's report", len(downloads), len(email.Attachments))
					}
					if !containsAny(texts, string(part.octets)) {
						t.Errorf("BODY[%s]'s octets %q are in no body part the filters were handed: %q", part.section, part.octets, texts)
					}
					if want := string([]rune(string(part.octets))); containsAny(jsonTexts, string(part.octets)) || !containsAny(jsonTexts, want) {
						t.Errorf("JSON filters were handed %q, want the octets with U+FFFD for what isn't UTF-8, %q", jsonTexts, want)
					}
					for name, body := range sent {
						if !strings.Contains(body, string(part.octets)) {
							t.Errorf("%s was sent %.600q, which doesn't hold BODY[%s]'s octets", name, body, part.section)
						}
					}
				}
			}
		})
	}
}

// filterTexts is every leaf of the body tree a filter is handed.
func filterTexts(body pipeline.Body) []string {
	if len(body.Parts) == 0 {
		return []string{body.Content}
	}
	var texts []string
	for _, part := range body.Parts {
		texts = append(texts, filterTexts(part)...)
	}
	return texts
}

func containsAny(texts []string, want string) bool {
	for _, text := range texts {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

// filterAttachment is the octets of the attachment or inline part a filter is handed by that name.
func filterAttachment(email *pipeline.EmailJSON, filename string) ([]byte, bool) {
	for _, list := range [][]pipeline.Attachment{email.Inline, email.Attachments} {
		for _, a := range list {
			if a.Filename == filename {
				octets, err := base64.StdEncoding.DecodeString(a.Content)
				return octets, err == nil
			}
		}
	}
	return nil, false
}

// unfolded drops line breaks, so base64 wrapped at any width compares.
func unfolded(s string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(s) }
