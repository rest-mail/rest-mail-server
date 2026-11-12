package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// stubScanner stands in for the ClamAV and rspamd services, and writes down what the real filters send
// them. It answers every message clean.
type stubScanner struct {
	mu      sync.Mutex
	clamav  string
	rspamd  string
	handler http.Handler
}

func newStubScanner() *stubScanner {
	s := &stubScanner{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("/scan", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.clamav = string(body)
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"status":"OK"}`))
	})
	mux.HandleFunc("/checkv2", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.rspamd = string(body)
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"action":"no action","score":0,"required_score":15}`))
	})
	s.handler = mux
	return s
}

// A field one reader takes and the other refuses can't hide a part: whatever a sender writes in a
// parameter field, the row, the attachment list, the filters and the scanners they call have the
// message a mail client is shown, from one reading of it. A raw octet that isn't UTF-8 refuses the
// whole message wherever it stands, even in another field (#1921), so that's read in
// TestFieldsReadAsBodyStructureTellsAClient instead.
func TestFieldFaultsReadTheSameEverywhere(t *testing.T) {
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize})
	var domain models.Domain
	if err := f.db.First(&domain, "name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	scanner := newStubScanner()
	scanners := httptest.NewServer(scanner.handler)
	t.Cleanup(scanners.Close)
	seen := &seenByFilters{}
	pipeline.DefaultRegistry.Register("seen_by_filters", func([]byte) (pipeline.Filter, error) { return seen, nil })
	// The scanners run before extract_attachments, which is where an operator puts them.
	filters := fmt.Sprintf(`[{"name":"seen_by_filters","type":"action","enabled":true},`+
		`{"name":"clamav","type":"action","enabled":true,"config":{"url":%q,"fallback_action":"reject"}},`+
		`{"name":"rspamd","type":"action","enabled":true,"config":{"url":%q,"fallback_action":"reject"}},`+
		`{"name":"extract_attachments","type":"transform","enabled":true,"config":{"storage_path":%q}}]`, scanners.URL, scanners.URL, t.TempDir())
	if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(filters), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	imap := openIMAPSession(t, f, apiclient.New(srv.URL))
	many := func(field string) string {
		var b strings.Builder
		for i := range 20 {
			fmt.Fprintf(&b, "; p%d=v", i)
		}
		return field + b.String()
	}
	const body = "--a\r\nContent-Type: text/plain\r\n\r\nvisible text\r\n--a\r\n"
	mixedType := func(contentType, part string) string {
		return repeatedHead + "Content-Type: " + contentType + "\r\n\r\n" + body + part + "\r\n\r\nATTACHED\r\n--a--\r\n"
	}
	mixed := func(top, part string) string { return mixedType("multipart/mixed; "+top, part) }
	const octets = "Content-Type: application/octet-stream"
	const attachedPart = octets + "\r\nContent-Disposition: attachment; filename=a.txt"
	disposed := func(field string) string { return mixed("boundary=a", octets+"\r\nContent-Disposition: "+field) }
	named := func(field string) string { return mixed("boundary=a", octets+"; "+field) }
	const attachedSeen = `tree=multipart/mixed[text/plain:"visible text"] attachment=`
	row := func(filename string) (string, string) {
		quoted := fmt.Sprintf("%q(application/octet-stream)", filename)
		return quoted, attachedSeen + quoted
	}
	type expectation struct {
		name, message string
		// What each reader has of it: the row's text, the attachment rows, the filters, a piece of
		// BODYSTRUCTURE, and the text of the section named, which a scanner has to have been sent too.
		text, attachments, filters, structure, section, sectionText string
	}
	asText := func(name, message string) expectation {
		sent := message[strings.Index(message, "\r\n\r\n")+4:]
		return expectation{name, message, strings.TrimSpace(sent), "", fmt.Sprintf("tree=text/plain:%q", sent), `"TEXT" "PLAIN" ("CHARSET" "US-ASCII")`, "1", "ATTACHED"}
	}
	attached := func(name, message, filename, structure string) expectation {
		rows, filtersSaw := row(filename)
		return expectation{name, message, "visible text", rows, filtersSaw, structure, "2", "ATTACHED"}
	}
	const aTxt = `("ATTACHMENT" ("FILENAME" "a.txt"`
	const anyOctets = `"APPLICATION" "OCTET-STREAM"`
	const notAttached = `tree=multipart/mixed[text/plain:"visible text" `
	rows := []expectation{
		// A multipart's parts, from a field go-imap reads past a fault in.
		attached("a comment after the boundary", mixed("boundary=a (comment)", attachedPart), "a.txt", `"MIXED" ("BOUNDARY" "a")`),
		attached("a comment before the boundary", mixed("(comment) boundary=a", attachedPart), "a.txt", `"MIXED" ("BOUNDARY" "a")`),
		attached("a comment, read a parameter at a time", mixed(many("boundary=a (comment)"), attachedPart), "a.txt", `"MIXED" ("BOUNDARY" "a" "P0"`),
		attached("a comment on the type", mixedType("multipart/mixed (comment); boundary=a", attachedPart), "a.txt", `"MIXED" ("BOUNDARY" "a")`),
		attached("spaces around the type's slash", mixedType("multipart / mixed; boundary=a", attachedPart), "a.txt", `"MIXED" ("BOUNDARY" "a")`),
		attached("an 8-bit parameter", mixed("boundary=a; x=caf\xc3\xa9", attachedPart), "a.txt", `"MIXED" ("BOUNDARY" "a" "X" "=?UTF-8?Q?caf=C3=A9?=")`),
		attached("an 8-bit parameter, read a parameter at a time", mixed(many("boundary=a; x=caf\xc3\xa9"), attachedPart), "a.txt", `"MIXED" ("BOUNDARY" "a"`),
		attached("a boundary in a charset no one reads", mixed("boundary*=x-none''a", attachedPart), "a.txt", `"MIXED" ("BOUNDARY" "a")`),
		attached("a boundary in Latin-1", mixed("boundary*=iso-8859-1''a", attachedPart), "a.txt", `"MIXED" ("BOUNDARY" "a")`),
		attached("a boundary spelled as an encoded word stays those octets", strings.ReplaceAll(mixed("boundary=\"=?utf-8?Q?a?=\"", attachedPart), "--a", "--=?utf-8?Q?a?="), "a.txt", `"MIXED" ("BOUNDARY" "=?utf-8?Q?a?=")`),
		// A field that gives the entity no type is RFC 2045 §5.2's default, so the whole body is text.
		asText("a later boundary that doesn't read", mixed("boundary=a; boundary=\"b", attachedPart)),
		asText("a later parameter that doesn't read", mixed("boundary=a; x=\"b", attachedPart)),
		asText("a comment that never closes", mixed("boundary=a (comment", attachedPart)),
		asText("a multipart with no boundary", mixedType("multipart/mixed", attachedPart)),
		asText("a boundary with no first segment", mixed("boundary*1=a", attachedPart)),
		// A part's name, from a field go-imap reads past a fault in.
		attached("a comment after the filename", disposed("attachment; filename=a.txt (comment)"), "a.txt", aTxt),
		attached("a comment before the filename", disposed("attachment; (comment) filename=a.txt"), "a.txt", aTxt),
		attached("a comment on the disposition", disposed("attachment (comment); filename=a.txt"), "a.txt", aTxt),
		attached("a comment, read a parameter at a time, on a filename", disposed(many("attachment; filename=a.txt (comment)")), "a.txt", aTxt),
		attached("another 8-bit parameter beside a filename", disposed("attachment; filename=a.txt; x=caf\xc3\xa9"), "a.txt", aTxt),
		attached("a UTF-8 filename", disposed("attachment; filename=a\xc3\xa9.txt"), "aé.txt", `("ATTACHMENT" ("FILENAME" "=?`),
		attached("a Latin-1 filename", disposed("attachment; filename*=iso-8859-1''a%E9.txt"), "aé.txt", `("ATTACHMENT" ("FILENAME" "=?`),
		attached("a Latin-1 filename that is ASCII", disposed("attachment; filename*=iso-8859-1''a.exe"), "a.exe", `("ATTACHMENT" ("FILENAME" "a.exe"`),
		attached("a filename in a charset that's read", disposed("attachment; filename*=windows-1252''a%E9.txt"), "aé.txt", `("ATTACHMENT" ("FILENAME" "=?windows-1252?Q?a=E9`),
		attached("a filename in a charset no one reads", disposed("attachment; filename*=x-none''a%E9.txt"), "=?x-none?Q?a=E9=2Etxt?=", `("ATTACHMENT" ("FILENAME" "=?x-none?Q?a=E9`),
		attached("extended octets that aren't UTF-8", disposed("attachment; filename*=utf-8''a%E9.txt"), "a\uFFFD.txt", `("ATTACHMENT" ("FILENAME" "=?UNKNOWN-8BIT?Q?a=E9`),
		// A NUL reads as a space in what's sent (#1921), so that's what the row and filters have too.
		attached("a NUL in a filename", disposed("attachment; filename*=utf-8''a%00.txt"), "a .txt", `("ATTACHMENT" ("FILENAME" "a .txt"`),
		attached("a NUL in an encoded word in a filename", disposed("attachment; filename=\"=?utf-8?Q?a=00.txt?=\""), "a .txt", `("ATTACHMENT" ("FILENAME" "=?utf-8?Q?a_.txt?="))`),
		attached("a NUL in a base64 encoded word in a filename", disposed("attachment; filename=\"=?utf-8?B?YQAudHh0?=\""), "a .txt", `("ATTACHMENT" ("FILENAME" "=?utf-8?B?YSAudHh0?="))`),
		attached("a NUL in an encoded word beside a UTF-8 character", disposed("attachment; filename=\"=?utf-8?Q?=C3=A9=00.txt?=\""), "é .txt", `("ATTACHMENT" ("FILENAME" "=?utf-8?Q?=C3=A9_.txt?="))`),
		attached("a NUL in the first of two encoded words", disposed("attachment; filename=\"=?utf-8?Q?a=00?= =?utf-8?Q?.txt?=\""), "a .txt", `("ATTACHMENT" ("FILENAME" "=?utf-8?Q?a_?= =?utf-8?Q?.txt?="))`),
		attached("a NUL in an encoded word in Windows-1252", disposed("attachment; filename=\"=?windows-1252?Q?a=00.txt?=\""), "a .txt", `("ATTACHMENT" ("FILENAME" "=?windows-1252?Q?a_.txt?="))`),
		attached("a NUL in an encoded word in UTF-16", disposed("attachment; filename=\"=?utf-16be?B?AGEAAAAu?=\""), "a .", `("ATTACHMENT" ("FILENAME" "=?utf-16be?B?AGEAIAAu?="))`),
		attached("a UTF-16 word with no NUL character", disposed("attachment; filename=\"=?utf-16be?B?AGEALg==?=\""), "a.", `("ATTACHMENT" ("FILENAME" "=?utf-16be?B?AGEALg==?="))`),
		attached("a NUL in an encoded word in a charset no one reads", disposed("attachment; filename=\"=?x-none?Q?a=00.txt?=\""), "=?x-none?Q?a_.txt?=", `("ATTACHMENT" ("FILENAME" "=?x-none?Q?a_.txt?="))`),
		attached("a NUL in a filename in a charset no one reads", disposed("attachment; filename*=x-none''a%00.txt"), "=?x-none?Q?a_=2Etxt?=", `("ATTACHMENT" ("FILENAME" "=?x-none?Q?a_=2Etxt?="))`),
		attached("a NUL in a filename in a charset that's read", disposed("attachment; filename*=windows-1252''a%00.txt"), "a .txt", `("ATTACHMENT" ("FILENAME" "=?windows-1252?Q?a_=2Etxt?="))`),
		attached("a NUL in a Latin-1 filename", disposed("attachment; filename*=iso-8859-1''a%00.txt"), "a .txt", `("ATTACHMENT" ("FILENAME" "a .txt"))`),
		attached("a NUL in an encoded word spelled in RFC 2231", disposed("attachment; filename*=utf-8''%3D%3Futf-8%3FQ%3Fa%3D00.txt%3F%3D"), "a .txt", `("ATTACHMENT" ("FILENAME" "=?utf-8?Q?a_.txt?="))`),
		attached("a NUL in an encoded word in a name", named("name=\"=?utf-8?Q?a=00.txt?=\""), "a .txt", `"OCTET-STREAM" ("NAME" "=?utf-8?Q?a_.txt?=")`),
		attached("a NUL in a base64 encoded word in a name", named("name=\"=?utf-8?B?YQAudHh0?=\""), "a .txt", `"OCTET-STREAM" ("NAME" "=?utf-8?B?YSAudHh0?=")`),
		attached("a NUL in a name", named("name*=utf-8''a%00.txt"), "a .txt", `"OCTET-STREAM" ("NAME" "a .txt")`),
		// Delimiter lines hold a boundary's octets and nothing decodes them, so it isn't spaced.
		attached("a NUL spelled in a boundary stays those octets", strings.ReplaceAll(mixed("boundary=\"=?utf-8?Q?a=00?=\"", attachedPart), "--a", "--=?utf-8?Q?a=00?="), "a.txt", `"MIXED" ("BOUNDARY" "=?utf-8?Q?a=00?=")`),
		attached("a filename with a later segment in Latin-1", disposed("attachment; filename*0*=iso-8859-1''a%E9; filename*1*=b.txt"), "aéb.txt", `("ATTACHMENT" ("FILENAME" "=?`),
		attached("a comment after a name", named("name=a.txt (comment)"), "a.txt", `"OCTET-STREAM" ("NAME" "a.txt")`),
		attached("a UTF-8 name", named("name=a\xc3\xa9.txt"), "aé.txt", `"OCTET-STREAM" ("NAME" "=?`),
		attached("a Latin-1 name", named("name*=iso-8859-1''a%E9.txt"), "aé.txt", `"OCTET-STREAM" ("NAME" "=?`),
		// A part with no readable fields is told as what's left of it, and is still handed over.
		{"a disposition that doesn't read", disposed("attachment; filename=\"a.txt"), "visible text", "", notAttached + `application/octet-stream:"ATTACHED"]`, anyOctets, "2", "ATTACHED"},
		{"a disposition with a slash", disposed("attachment/x; x=1"), "visible text", "", notAttached + `application/octet-stream:"ATTACHED"]`, anyOctets, "2", "ATTACHED"},
		{"a part's type that doesn't read", named("name=a.txt; x=\"b"), "visible text", "", notAttached + `text/plain:"ATTACHED"]`, `"TEXT" "PLAIN" ("CHARSET" "US-ASCII")`, "2", "ATTACHED"},
		{"a part naming a type with no subtype", mixed("boundary=a", "Content-Type: application; name=a.txt"), "visible text", "", notAttached + `text/plain:"ATTACHED"]`, `"TEXT" "PLAIN" ("CHARSET" "US-ASCII")`, "2", "ATTACHED"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if err := f.db.Where("mailbox_id = ?", f.mailboxID).Delete(&models.Message{}).Error; err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(map[string]any{"address": f.address, "sender": "sender@remote.test", "recipients_to": []string{f.address},
				"client_ip": "192.0.2.1", "helo_name": "mail.remote.test", "received_tls": true, "raw_message": base64.StdEncoding.EncodeToString([]byte(row.message))})
			if err != nil {
				t.Fatal(err)
			}
			if answer := f.do(http.MethodPost, "/api/v1/messages/deliver", "", string(payload)); answer.Code != http.StatusCreated {
				t.Fatalf("refused: %d %s", answer.Code, answer.Body)
			}
			// The bytes are stored as they came, whatever a reader makes of them.
			stored := lastStored(t, f, row.message)
			var attachmentRows []models.Attachment
			if err := f.db.Where("message_id = ?", stored.ID).Order("id").Find(&attachmentRows).Error; err != nil {
				t.Fatal(err)
			}
			var attachments []string
			for _, attachment := range attachmentRows {
				attachments = append(attachments, fmt.Sprintf("%q(%s)", attachment.Filename, attachment.ContentType))
			}
			if got := strings.TrimSpace(stored.BodyText); got != row.text {
				t.Errorf("row's text %q, want %q", got, row.text)
			}
			if got := strings.Join(attachments, " "); got != row.attachments {
				t.Errorf("attachment rows %s, want %s", got, row.attachments)
			}
			if got := seen.describe(); got != row.filters {
				t.Errorf("filters were handed %s, want %s", got, row.filters)
			}
			imap.do("SELECT INBOX")
			if structure := imap.do("FETCH 1 (BODYSTRUCTURE)"); !strings.Contains(structure, row.structure) {
				t.Errorf("BODYSTRUCTURE %s, want %s in it", structure, row.structure)
			}
			if got := imap.do("FETCH 1 (BODY.PEEK[" + row.section + "])"); !strings.Contains(got, row.sectionText) {
				t.Errorf("BODY[%s] %s, want %q in it", row.section, got, row.sectionText)
			}
			// What a scanner is sent holds that part, in the base64 an attachment travels in or as text.
			encoded := base64.StdEncoding.EncodeToString([]byte(row.sectionText))
			scanner.mu.Lock()
			clamav, rspamd := scanner.clamav, scanner.rspamd
			scanner.mu.Unlock()
			for name, sent := range map[string]string{"clamav": clamav, "rspamd": rspamd} {
				if !strings.Contains(sent, row.sectionText) && !strings.Contains(sent, encoded) {
					t.Errorf("%s was sent %q, which doesn't hold the part", name, sent)
				}
			}
		})
	}
}

// A display name reads the same for a filter as for a mail client, and no name costs a filter the
// addresses beside it, which is how a sender would hide from a rule. A client is sent the name's own
// spelling where it can't be decoded here, and decodes any word it is sent, so a word in a quoted
// string is read too, though RFC 2047 §5 doesn't allow one there. Each row is the exact ENVELOPE field
// and the exact list a filter is handed.
func TestAddressNamesReadTheSameEverywhere(t *testing.T) {
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
	imap := openIMAPSession(t, f, apiclient.New(srv.URL))
	const aSent = `"a" "example.com"`
	one := func(name string) string { return `((` + name + ` NIL ` + aSent + `))` }
	listed := func(name string) string { return fmt.Sprintf("{%q a@example.com}", name) }
	for _, row := range []struct {
		name, from, envelope, filters string
	}{
		// A charset nobody here reads.
		{"a name in a charset no one reads", `=?x-none?Q?ab?= <a@example.com>`, one(`"=?x-none?Q?ab?="`), listed("=?x-none?Q?ab?=")},
		{"a NUL in a name in a charset no one reads", `=?x-none?Q?a=00b?= <a@example.com>`, one(`"=?x-none?Q?a_b?="`), listed("=?x-none?Q?a_b?=")},
		{"a base64 name in a charset no one reads", `=?x-none?B?YWI=?= <a@example.com>`, one(`"=?x-none?B?YWI=?="`), listed("=?x-none?B?YWI=?=")},
		{"the addresses after such a name", `=?x-none?Q?ab?= <a@example.com>, C <c@example.com>`, `(("=?x-none?Q?ab?=" NIL ` + aSent + `)("C" NIL "c" "example.com"))`, `{"=?x-none?Q?ab?=" a@example.com} {"C" c@example.com}`},
		{"the addresses before such a name", `C <c@example.com>, =?x-none?Q?ab?= <a@example.com>`, `(("C" NIL "c" "example.com")("=?x-none?Q?ab?=" NIL ` + aSent + `))`, `{"C" c@example.com} {"=?x-none?Q?ab?=" a@example.com}`},
		{"such a word after a word that's read", `=?utf-8?Q?x?= =?x-none?Q?ab?= <a@example.com>`, one(`"x =?x-none?Q?ab?="`), listed("x =?x-none?Q?ab?=")},
		{"such a word before words that are read", `=?x-none?Q?ab?= =?utf-8?Q?x?= =?utf-8?Q?y?= <a@example.com>`, one(`"=?x-none?Q?ab?= xy"`), listed("=?x-none?Q?ab?= xy")},
		{"such a word as a comment's name", `a@example.com (=?x-none?Q?ab?=)`, one(`"=?x-none?Q?ab?="`), listed("=?x-none?Q?ab?=")},
		{"such a word as a group's name", `=?x-none?Q?ab?=: a@example.com;`, `((NIL NIL "=?x-none?Q?ab?=" NIL)(NIL NIL ` + aSent + `)(NIL NIL NIL NIL))`, listed("")},
		// RFC 2231 §5 lets a language follow a word's charset, which neither side reads.
		{"a language on a word's charset", `=?utf-8*en?Q?ab?= <a@example.com>`, one(`"=?utf-8*en?Q?ab?="`), listed("=?utf-8*en?Q?ab?=")},
		// UTF-7 is no charset either side reads, and spells U+0000 in its own base64.
		{"a NUL in a UTF-7 name stays spelled", `=?utf-7?Q?a+AAA-b?= <a@example.com>`, one(`"=?utf-7?Q?a+AAA-b?="`), listed("=?utf-7?Q?a+AAA-b?=")},
		// A word in a quoted string, which a client decodes all the same.
		{"a NUL in a quoted word", `"=?utf-8?Q?a=00b?=" <a@example.com>`, one(`"=?utf-8?Q?a_b?="`), listed("a b")},
		{"a NUL in a quoted base64 word", `"=?utf-8?B?YQBi?=" <a@example.com>`, one(`"=?utf-8?B?YSBi?="`), listed("a b")},
		{"a quoted word", `"=?utf-8?Q?ab?=" <a@example.com>`, one(`"=?utf-8?Q?ab?="`), listed("ab")},
		{"a quoted word in Latin-1", `"=?iso-8859-1?Q?caf=E9?=" <a@example.com>`, one(`"=?iso-8859-1?Q?caf=E9?="`), listed("café")},
		{"a quoted word in Windows-1252", `"=?windows-1252?Q?caf=E9?=" <a@example.com>`, one(`"=?windows-1252?Q?caf=E9?="`), listed("café")},
		{"a quoted word among text", `"x =?utf-8?Q?a=00b?= y" <a@example.com>`, one(`"x =?utf-8?Q?a_b?= y"`), listed("x a b y")},
		{"two quoted words", `"=?utf-8?Q?a?= =?utf-8?Q?b?=" <a@example.com>`, one(`"=?utf-8?Q?a?= =?utf-8?Q?b?="`), listed("ab")},
		{"a quoted word in a charset no one reads", `"=?x-none?Q?a=00b?=" <a@example.com>`, one(`"=?x-none?Q?a_b?="`), listed("=?x-none?Q?a_b?=")},
		{"a quoted word beside one in a charset no one reads", `"=?x-none?Q?a?= =?utf-8?Q?b?= =?utf-8?Q?c?=" <a@example.com>`, one(`"=?x-none?Q?a?= =?utf-8?Q?b?= =?utf-8?Q?c?="`), listed("=?x-none?Q?a?= bc")},
		{"a word a word decodes to", `=?utf-8?Q?=3D=3Futf-8=3FQ=3Fab=3F=3D?= <a@example.com>`, one(`"=?utf-8?Q?ab?="`), listed("ab")},
		// Names that were already read alike stay so.
		{"a word that isn't quoted", `=?utf-8?Q?a=00b?= <a@example.com>`, one(`"a b"`), listed("a b")},
		{"quoted text that isn't a word", `"=?utf-8?Q?a" <a@example.com>`, one(`"=?utf-8?Q?a"`), listed("=?utf-8?Q?a")},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := f.db.Where("mailbox_id = ?", f.mailboxID).Delete(&models.Message{}).Error; err != nil {
				t.Fatal(err)
			}
			message := "From: " + row.from + "\r\nTo: chris@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n\r\nbody\r\n"
			payload, err := json.Marshal(map[string]any{"address": f.address, "sender": "sender@remote.test", "recipients_to": []string{f.address},
				"client_ip": "192.0.2.1", "helo_name": "mail.remote.test", "received_tls": true, "raw_message": base64.StdEncoding.EncodeToString([]byte(message))})
			if err != nil {
				t.Fatal(err)
			}
			if answer := f.do(http.MethodPost, "/api/v1/messages/deliver", "", string(payload)); answer.Code != http.StatusCreated {
				t.Fatalf("refused: %d %s", answer.Code, answer.Body)
			}
			lastStored(t, f, message)
			seen.mu.Lock()
			headers := seen.email.Headers
			seen.mu.Unlock()
			var from []string
			for _, address := range headers.From.All() {
				from = append(from, fmt.Sprintf("{%q %s}", address.Name, address.Address))
			}
			if got := strings.Join(from, " "); got != row.filters {
				t.Errorf("filters were handed From %s, want %s", got, row.filters)
			}
			imap.do("SELECT INBOX")
			envelope := imap.do("FETCH 1 (ENVELOPE)")
			want := `ENVELOPE ("Thu, 02 Jan 2020 03:04:05 +0000" "s" ` + row.envelope + ` ` + row.envelope + ` ` + row.envelope + ` ((NIL NIL "chris" "example.test")) NIL NIL NIL NIL)`
			if !strings.Contains(envelope, want) {
				t.Errorf("sent %s, want %s", envelope, want)
			}
		})
	}
}

// Every field that holds addresses keeps them past such a name, and a UTF-7 Subject that spells a NUL
// goes out and reads as it was written, since neither side reads that charset.
func TestAddressFieldsKeepTheirAddressesPastAName(t *testing.T) {
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
	imap := openIMAPSession(t, f, apiclient.New(srv.URL))
	message := "From: =?x-none?Q?f?= <from@example.com>\r\nTo: =?x-none?Q?t?= <to@example.com>, chris@example.test\r\nCc: =?x-none?Q?c?= <cc@example.com>\r\n" +
		"Subject: =?utf-7?Q?a+AAA-b?=\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n\r\nbody\r\n"
	payload, err := json.Marshal(map[string]any{"address": f.address, "sender": "sender@remote.test", "recipients_to": []string{f.address},
		"client_ip": "192.0.2.1", "helo_name": "mail.remote.test", "received_tls": true, "raw_message": base64.StdEncoding.EncodeToString([]byte(message))})
	if err != nil {
		t.Fatal(err)
	}
	if answer := f.do(http.MethodPost, "/api/v1/messages/deliver", "", string(payload)); answer.Code != http.StatusCreated {
		t.Fatalf("refused: %d %s", answer.Code, answer.Body)
	}
	seen.mu.Lock()
	headers := seen.email.Headers
	seen.mu.Unlock()
	for _, field := range []struct {
		name string
		list pipeline.AddressList
		want []pipeline.Address
	}{
		{"From", headers.From, []pipeline.Address{{Name: "=?x-none?Q?f?=", Address: "from@example.com"}}},
		{"To", headers.To, []pipeline.Address{{Name: "=?x-none?Q?t?=", Address: "to@example.com"}, {Address: "chris@example.test"}}},
		{"Cc", headers.Cc, []pipeline.Address{{Name: "=?x-none?Q?c?=", Address: "cc@example.com"}}},
	} {
		if got := field.list.All(); !reflect.DeepEqual(got, field.want) {
			t.Errorf("filters were handed %s %+v, want %+v", field.name, got, field.want)
		}
	}
	if want := "=?utf-7?Q?a+AAA-b?="; headers.Subject != want {
		t.Errorf("filters were handed Subject %q, want %q", headers.Subject, want)
	}
	imap.do("SELECT INBOX")
	const want = `ENVELOPE ("Thu, 02 Jan 2020 03:04:05 +0000" "=?utf-7?Q?a+AAA-b?=" (("=?x-none?Q?f?=" NIL "from" "example.com")) (("=?x-none?Q?f?=" NIL "from" "example.com")) (("=?x-none?Q?f?=" NIL "from" "example.com")) (("=?x-none?Q?t?=" NIL "to" "example.com")(NIL NIL "chris" "example.test")) (("=?x-none?Q?c?=" NIL "cc" "example.com")) NIL NIL NIL)`
	if got := imap.do("FETCH 1 (ENVELOPE)"); !strings.Contains(got, want) {
		t.Errorf("sent %s, want %s", got, want)
	}
}
