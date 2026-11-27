package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/pkg/apiclient"
)

// encodedNamesFixture is a sender's whole path into a mailbox and back out: the real SMTP gateway
// delivering through the production router into PostgreSQL, a filter that writes down what the
// pipeline hands every filter, and two clients of the real IMAP gateway, one of which has enabled
// UTF8=ACCEPT (RFC 6855).
type encodedNamesFixture struct {
	f      *credentialFixture
	seen   *seenByFilters
	smtp   string
	legacy *imapSession
	utf8   *imapSession
}

func newEncodedNamesFixture(t *testing.T) *encodedNamesFixture {
	t.Helper()
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize})
	var domain models.Domain
	if err := f.db.First(&domain, "name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	seen := &seenByFilters{}
	pipeline.DefaultRegistry.Register("seen_by_filters", func([]byte) (pipeline.Filter, error) { return seen, nil })
	if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(`[{"name":"seen_by_filters","type":"action","enabled":true}]`), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gateway := smtpgw.NewServer("mail.example.test", apiclient.New(srv.URL), nil,
		smtpgw.NewStore(f.db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 100}))
	if err := gateway.Serve(smtpgw.SMTPListeners{Inbound: free}); err != nil {
		t.Fatalf("start the SMTP gateway: %v", err)
	}
	t.Cleanup(gateway.Shutdown)

	utf8 := openIMAPSession(t, f, f.gatewayClient(srv.URL, imapCN))
	for _, command := range []string{"CLOSE", "ENABLE UTF8=ACCEPT", "SELECT INBOX"} {
		if answer := utf8.do(command); !strings.Contains(answer, " OK") {
			t.Fatalf("%s: %s", command, answer)
		}
	}
	return &encodedNamesFixture{f: f, seen: seen, smtp: free.Addr().String(), legacy: openIMAPSession(t, f, f.gatewayClient(srv.URL, imapCN)), utf8: utf8}
}

// send hands message to the SMTP gateway as a remote server would, with the mailbox empty first, and
// answers the stored row, checked to hold exactly the octets sent.
func (x *encodedNamesFixture) send(t *testing.T, mailFrom, message string) models.Message {
	t.Helper()
	if err := x.f.db.Where("mailbox_id = ?", x.f.mailboxID).Delete(&models.Message{}).Error; err != nil {
		t.Fatal(err)
	}
	client, err := smtp.Dial(x.smtp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Hello("client.test"); err != nil {
		t.Fatal(err)
	}
	if err := send(client, mailFrom, x.f.address, message); err != nil {
		t.Fatalf("send: %v", err)
	}
	return lastStored(t, x.f, message)
}

// handed is what the pipeline handed the filters of field, one {"name" address} per address.
func (x *encodedNamesFixture) handed(field func(pipeline.Headers) pipeline.AddressList) string {
	x.seen.mu.Lock()
	defer x.seen.mu.Unlock()
	if x.seen.email == nil {
		return "(no filter ran)"
	}
	var out []string
	for _, address := range field(x.seen.email.Headers).All() {
		out = append(out, fmt.Sprintf("{%q %s}", address.Name, address.Address))
	}
	return strings.Join(out, " ")
}

// onlyLiteral is the one literal an IMAP response holds.
func onlyLiteral(t *testing.T, response string) string {
	t.Helper()
	open := strings.Index(response, "}\r\n")
	if open < 0 || strings.Count(response, "}\r\n") != 1 {
		t.Fatalf("not one literal in %q", response)
	}
	var size int
	if _, err := fmt.Sscanf(response[strings.LastIndex(response[:open], "{")+1:open], "%d", &size); err != nil {
		t.Fatalf("literal size in %q: %v", response, err)
	}
	return response[open+3 : open+3+size]
}

// listedSender is the sender_name the REST API answers for the message.
func (x *encodedNamesFixture) listedSender(t *testing.T, id uint) string {
	t.Helper()
	answer := x.f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", id), x.f.session, "")
	var read struct {
		Data struct {
			SenderName string `json:"sender_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(answer.Body.Bytes(), &read); err != nil || answer.Code != http.StatusOK {
		t.Fatalf("GET message %d: %d %.300s (%v)", id, answer.Code, answer.Body, err)
	}
	return read.Data.SenderName
}

// fetch is one FETCH of message 1 in both sessions, each with the mailbox opened again so that the
// new message is there.
func (x *encodedNamesFixture) fetch(item string) (legacy, utf8 string) {
	x.legacy.do("SELECT INBOX")
	x.utf8.do("SELECT INBOX")
	return x.legacy.do("FETCH 1 (" + item + ")"), x.utf8.do("FETCH 1 (" + item + ")")
}

// search is one SEARCH in both sessions. The legacy one names its charset; after UTF8=ACCEPT a
// CHARSET is not allowed (RFC 6855 §3), and the string is UTF-8 all the same.
func (x *encodedNamesFixture) search(key string) map[string]string {
	return map[string]string{"legacy": x.legacy.do("SEARCH CHARSET UTF-8 " + key), "UTF8=ACCEPT": x.utf8.do("SEARCH " + key)}
}

// deliver hands message to the delivery route directly, past the SMTP gateway's checks, with the
// mailbox empty first, and answers the stored row, checked to hold exactly the octets sent.
func (x *encodedNamesFixture) deliver(t *testing.T, message string) models.Message {
	t.Helper()
	if err := x.f.db.Where("mailbox_id = ?", x.f.mailboxID).Delete(&models.Message{}).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"address": x.f.address, "sender": "envelope@remote.test", "recipients_to": []string{x.f.address},
		"raw_message": base64.StdEncoding.EncodeToString([]byte(message))})
	if err != nil {
		t.Fatal(err)
	}
	if answer := x.f.do(http.MethodPost, "/api/v1/messages/deliver", "", string(payload)); answer.Code != http.StatusCreated {
		t.Fatalf("refused: %d %s", answer.Code, answer.Body)
	}
	return lastStored(t, x.f, message)
}

// TestEncodedAddressNamesThroughTheGateways is #1885's report and the clauses around it, sent through
// the SMTP gateway into PostgreSQL and read back over the IMAP gateway, the REST API and the pipeline.
// An ENVELOPE address used to read NIL when its personal or group name was an encoded word in a
// charset other than UTF-8, ISO-8859-1 or US-ASCII, because net/mail failed the whole list: the
// address, every other address in its field, and Sender and Reply-To, which default to From. The
// expected names are the charsets' own code points and the words as sent, not this server's output.
func TestEncodedAddressNamesThroughTheGateways(t *testing.T) {
	x := newEncodedNamesFixture(t)
	const date = "Date: Thu, 02 Jan 2020 03:04:05 +0000\r\n"

	t.Run("#1885's reproducer: a Windows-1252 From keeps its address, Sender and Reply-To", func(t *testing.T) {
		const message = "From: =?windows-1252?Q?J=F6rg?= <j@example.com>\r\nTo: chris@example.test\r\nSubject: s\r\n" + date + "\r\nbody\r\n"
		stored := x.send(t, "j@example.com", message)
		const from = `(("=?windows-1252?Q?J=F6rg?=" NIL "j" "example.com"))`
		const want = `"s" ` + from + ` ` + from + ` ` + from + ` ((NIL NIL "chris" "example.test")) NIL NIL NIL NIL)`
		legacy, utf8 := x.fetch("ENVELOPE")
		for session, got := range map[string]string{"legacy": legacy, "UTF8=ACCEPT": utf8} {
			if !strings.Contains(got, want) {
				t.Errorf("%s ENVELOPE %s\n  want %s in it", session, got, want)
			}
		}
		if got, want := x.handed(func(h pipeline.Headers) pipeline.AddressList { return h.From }), `{"Jörg" j@example.com}`; got != want {
			t.Errorf("filters were handed From %s, want %s", got, want)
		}
		if stored.SenderName != "Jörg" {
			t.Errorf("stored sender_name %q, want %q", stored.SenderName, "Jörg")
		}
		if got := x.listedSender(t, stored.ID); got != "Jörg" {
			t.Errorf("REST sender_name %q, want %q", got, "Jörg")
		}
		// SEARCH FROM compares the decoded name the row holds; the word as stored is not that name.
		for _, search := range []struct{ key, want string }{
			{`FROM "J` + "ö" + `rg"`, "* SEARCH 1\r\n"},
			{`FROM "j@example.com"`, "* SEARCH 1\r\n"},
			{`FROM "=?windows-1252?Q?J=F6rg?="`, "* SEARCH\r\n"},
		} {
			for session, got := range x.search(search.key) {
				if !strings.HasPrefix(got, search.want) {
					t.Errorf("%s SEARCH %s: %q, want %q", session, search.key, got, search.want)
				}
			}
		}
	})

	t.Run("names and a group in seven charsets keep every address in To and Cc", func(t *testing.T) {
		const message = "From: =?ISO-8859-2?Q?=A3ukasz?= <l@example.com>\r\n" +
			"To: =?KOI8-R?Q?=F0=D2=C9=D7=C5=D4?= <k@example.com>, =?ISO-2022-JP?B?GyRCRnxLXDhsGyhC?= <jp@example.com>, chris@example.test\r\n" +
			"Cc: =?ISO-8859-2?Q?Gr=F9?=: =?GB18030?B?1tDOxA==?= <g@example.com>, =?x-no-such-charset?Q?J=F6rg?= <x@example.com>;, " +
			"=?UNKNOWN-8BIT?Q?J=F6rg?= <u@example.com>, =?Shift_JIS?B?k4yLng==?= <s@example.com>\r\n" +
			"Subject: s\r\n" + date + "\r\nbody\r\n"
		stored := x.send(t, "l@example.com", message)
		const from = `(("=?ISO-8859-2?Q?=A3ukasz?=" NIL "l" "example.com"))`
		const want = `"s" ` + from + ` ` + from + ` ` + from + ` ` +
			`(("=?KOI8-R?Q?=F0=D2=C9=D7=C5=D4?=" NIL "k" "example.com")("=?ISO-2022-JP?B?GyRCRnxLXDhsGyhC?=" NIL "jp" "example.com")(NIL NIL "chris" "example.test")) ` +
			`((NIL NIL "=?ISO-8859-2?Q?Gr=F9?=" NIL)("=?GB18030?B?1tDOxA==?=" NIL "g" "example.com")("=?x-no-such-charset?Q?J=F6rg?=" NIL "x" "example.com")(NIL NIL NIL NIL)` +
			`("=?UNKNOWN-8BIT?Q?J=F6rg?=" NIL "u" "example.com")("=?Shift_JIS?B?k4yLng==?=" NIL "s" "example.com")) NIL NIL NIL)`
		legacy, utf8 := x.fetch("ENVELOPE")
		for session, got := range map[string]string{"legacy": legacy, "UTF8=ACCEPT": utf8} {
			if !strings.Contains(got, want) {
				t.Errorf("%s ENVELOPE %s\n  want %s in it", session, got, want)
			}
		}
		for _, field := range []struct {
			name string
			list func(pipeline.Headers) pipeline.AddressList
			want string
		}{
			{"From", func(h pipeline.Headers) pipeline.AddressList { return h.From }, `{"Łukasz" l@example.com}`},
			{"To", func(h pipeline.Headers) pipeline.AddressList { return h.To }, `{"Привет" k@example.com} {"日本語" jp@example.com} {"" chris@example.test}`},
			{"Cc", func(h pipeline.Headers) pipeline.AddressList { return h.Cc },
				`{"中文" g@example.com} {"=?x-no-such-charset?Q?J=F6rg?=" x@example.com} {"=?UNKNOWN-8BIT?Q?J=F6rg?=" u@example.com} {"東京" s@example.com}`},
		} {
			if got := x.handed(field.list); got != field.want {
				t.Errorf("filters were handed %s %s, want %s", field.name, got, field.want)
			}
		}
		if stored.SenderName != "Łukasz" {
			t.Errorf("stored sender_name %q, want %q", stored.SenderName, "Łukasz")
		}
		if got := x.listedSender(t, stored.ID); got != "Łukasz" {
			t.Errorf("REST sender_name %q, want %q", got, "Łukasz")
		}
		var to, cc []string
		if json.Unmarshal(stored.RecipientsTo, &to) != nil || json.Unmarshal(stored.RecipientsCc, &cc) != nil ||
			strings.Join(to, " ") != "k@example.com jp@example.com chris@example.test" ||
			strings.Join(cc, " ") != "g@example.com x@example.com u@example.com s@example.com" {
			t.Errorf("stored recipients_to %s and recipients_cc %s, want every address", stored.RecipientsTo, stored.RecipientsCc)
		}
		for session, got := range x.search(`FROM "` + "Ł" + `ukasz"`) {
			if !strings.HasPrefix(got, "* SEARCH 1\r\n") {
				t.Errorf("%s SEARCH FROM Łukasz: %q", session, got)
			}
		}
	})

	t.Run("a name of octets that are not UTF-8, and decoded text beside a kept word", func(t *testing.T) {
		const message = "From: J\xf6rg <j@example.com>, Ann <a@example.com>\r\n" +
			"To: =?UTF-8?Q?M=C3=BCller?= =?KOI8-R?B?7c/Ty9fB?= Ann <m@example.com>, chris@example.test\r\n" +
			"Subject: s\r\n" + date + "\r\nbody\r\n"
		// The SMTP gateway refuses header octets that are not UTF-8, so such a name is handed to the
		// delivery route, which stores any octets.
		client, err := smtp.Dial(x.smtp)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = client.Close() }()
		if err := send(client, "j@example.com", x.f.address, message); smtpCode(err) != 550 {
			t.Fatalf("SMTP took a header that is not UTF-8: %v", err)
		}
		x.deliver(t, message)
		const from = `(("=?UNKNOWN-8BIT?Q?J=F6rg?=" NIL "j" "example.com")("Ann" NIL "a" "example.com"))`
		legacy, utf8 := x.fetch("ENVELOPE")
		for session, got := range map[string]string{"legacy": legacy, "UTF8=ACCEPT": utf8} {
			if want := `"s" ` + from + ` ` + from + ` ` + from + ` `; !strings.Contains(got, want) {
				t.Errorf("%s ENVELOPE %s\n  want %s in it", session, got, want)
			}
		}
		// The text decoded here follows the session's rule; the KOI8-R word go-imap kept stays as sent.
		if want := `(("=?UTF-8?Q?M=C3=BCller?= =?KOI8-R?B?7c/Ty9fB?= Ann" NIL "m" "example.com")(NIL NIL "chris" "example.test"))`; !strings.Contains(legacy, want) {
			t.Errorf("legacy ENVELOPE %s\n  want %s in it", legacy, want)
		}
		if want := "Müller =?KOI8-R?B?7c/Ty9fB?= Ann"; onlyLiteral(t, utf8) != want || !strings.Contains(utf8, `NIL "m" "example.com")(NIL NIL "chris" "example.test"))`) {
			t.Errorf("UTF8=ACCEPT ENVELOPE %q\n  want the name %q", utf8, want)
		}
		if got, want := x.handed(func(h pipeline.Headers) pipeline.AddressList { return h.To }), `{"MüllerМосква Ann" m@example.com} {"" chris@example.test}`; got != want {
			t.Errorf("filters were handed To %s, want %s", got, want)
		}
	})

	t.Run("a From of octets that are not UTF-8 keeps every address, in any position", func(t *testing.T) {
		// Folded on the way out, the long name is four words, which a client reads unfolded.
		eight := `=?UNKNOWN-8BIT?Q?` + strings.Repeat("J=F6rg_", 8) + `?= `
		longName := strings.Repeat(eight, 3) + `=?UNKNOWN-8BIT?Q?` + strings.TrimSuffix(strings.Repeat("J=F6rg_", 6), "_") + `?=`
		const ann = `("Ann" NIL "a" "example.com")`
		for _, row := range []struct{ name, from, envelope string }{
			{"in a quoted string with a comma", `"J` + "\xf6" + `rg, Sr" <j@example.com>, Ann <a@example.com>`,
				`(("=?UNKNOWN-8BIT?Q?J=F6rg=2C_Sr?=" NIL "j" "example.com")` + ann + `)`},
			{"in a comment", "j@example.com (J\xf6rg), Ann <a@example.com>", `(("=?UNKNOWN-8BIT?Q?J=F6rg?=" NIL "j" "example.com")` + ann + `)`},
			{"in a group's name", "Gr\xf9: j@example.com;, Ann <a@example.com>",
				`((NIL NIL "=?UNKNOWN-8BIT?Q?Gr=F9?=" NIL)(NIL NIL "j" "example.com")(NIL NIL NIL NIL)` + ann + `)`},
			{"beside UTF-8", "J\xc3\xb6rg J\xf6rg <j@example.com>, Ann <a@example.com>", `(("=?UNKNOWN-8BIT?Q?J=C3=B6rg_J=F6rg?=" NIL "j" "example.com")` + ann + `)`},
			{"long enough to be folded", strings.Repeat("J\xf6rg ", 30) + "<j@example.com>, Ann <a@example.com>", `(("` + longName + `" NIL "j" "example.com")` + ann + `)`},
		} {
			t.Run(row.name, func(t *testing.T) {
				x.deliver(t, "From: "+row.from+"\r\nTo: chris@example.test\r\nSubject: s\r\n"+date+"\r\nbody\r\n")
				want := `"s" ` + row.envelope + ` ` + row.envelope + ` ` + row.envelope + ` ((NIL NIL "chris" "example.test")) NIL NIL NIL NIL)`
				legacy, utf8 := x.fetch("ENVELOPE")
				for session, got := range map[string]string{"legacy": legacy, "UTF8=ACCEPT": utf8} {
					if !strings.Contains(got, want) {
						t.Errorf("%s ENVELOPE %s\n  want %s in it", session, got, want)
					}
				}
			})
		}
	})

	t.Run("an encapsulated message's ENVELOPE, as a part and as the whole body", func(t *testing.T) {
		const inner = "From: =?windows-1252?Q?J=F6rg?= <j@example.com>\r\n" +
			"To: =?ISO-8859-2?Q?Gr=F9?=: =?KOI8-R?B?7c/Ty9fB?= <m@example.com>;\r\n" +
			"Cc: J\xf6rg <raw@example.com>, =?GB18030?Q?=D6=D0?= <g@example.com>\r\n" +
			"Subject: inner\r\n" + date + "\r\ninner body\r\n"
		const from = `(("=?windows-1252?Q?J=F6rg?=" NIL "j" "example.com"))`
		const want = `"inner" ` + from + ` ` + from + ` ` + from + ` ` +
			`((NIL NIL "=?ISO-8859-2?Q?Gr=F9?=" NIL)("=?KOI8-R?B?7c/Ty9fB?=" NIL "m" "example.com")(NIL NIL NIL NIL)) ` +
			`(("=?UNKNOWN-8BIT?Q?J=F6rg?=" NIL "raw" "example.com")("=?GB18030?Q?=D6=D0?=" NIL "g" "example.com")) NIL NIL NIL)`
		const outer = "From: Outer <o@example.com>\r\nTo: chris@example.test\r\nSubject: fwd\r\n" + date + "MIME-Version: 1.0\r\n"
		for _, shape := range []struct{ name, message string }{
			{"the whole body", outer + "Content-Type: message/rfc822\r\n\r\n" + inner},
			{"a part", outer + "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nsee below\r\n" +
				"--b\r\nContent-Type: message/rfc822\r\n\r\n" + inner + "--b--\r\n"},
		} {
			t.Run(shape.name, func(t *testing.T) {
				x.send(t, "o@example.com", shape.message)
				legacy, utf8 := x.fetch("BODYSTRUCTURE")
				for session, got := range map[string]string{"legacy": legacy, "UTF8=ACCEPT": utf8} {
					if !strings.Contains(got, want) {
						t.Errorf("%s BODYSTRUCTURE %s\n  want %s in it", session, got, want)
					}
				}
			})
		}
	})
}
