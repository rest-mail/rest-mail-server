package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// rfc2231Fixture is the production router on real PostgreSQL, with an inbound pipeline that stores
// attachments and writes down what the filters are handed, and a client of the real IMAP gateway.
type rfc2231Fixture struct {
	f    *credentialFixture
	seen *seenByFilters
	imap *imapSession
	srv  *httptest.Server
}

func newRFC2231Fixture(t *testing.T) *rfc2231Fixture {
	t.Helper()
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize})
	var domain models.Domain
	if err := f.db.First(&domain, "name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	seen := &seenByFilters{}
	pipeline.DefaultRegistry.Register("seen_by_filters", func([]byte) (pipeline.Filter, error) { return seen, nil })
	filters := fmt.Sprintf(`[{"name":"seen_by_filters","type":"action","enabled":true},`+
		`{"name":"extract_attachments","type":"transform","enabled":true,"config":{"storage_path":%q}}]`, t.TempDir())
	if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(filters), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	return &rfc2231Fixture{f: f, seen: seen, srv: srv, imap: openIMAPSession(t, f, f.gatewayClient(srv.URL, imapCN))}
}

// deliver hands message to the delivery route, as the SMTP gateway does, and answers the stored row,
// checked to hold exactly the octets sent.
func (x *rfc2231Fixture) deliver(t *testing.T, message string) models.Message {
	t.Helper()
	if err := x.f.db.Where("mailbox_id = ?", x.f.mailboxID).Delete(&models.Message{}).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"address": x.f.address, "sender": "sender@remote.test", "recipients_to": []string{x.f.address},
		"client_ip": "192.0.2.1", "helo_name": "mail.remote.test", "received_tls": true, "raw_message": base64.StdEncoding.EncodeToString([]byte(message))})
	if err != nil {
		t.Fatal(err)
	}
	if answer := x.f.do(http.MethodPost, "/api/v1/messages/deliver", "", string(payload)); answer.Code != http.StatusCreated {
		t.Fatalf("refused: %d %s", answer.Code, answer.Body)
	}
	return lastStored(t, x.f, message)
}

// literal is the one literal an IMAP response holds.
func literal(t *testing.T, response string) string {
	t.Helper()
	open := strings.Index(response, "}\r\n")
	if open < 0 {
		t.Fatalf("no literal in %q", response)
	}
	var size int
	if _, err := fmt.Sscanf(response[strings.LastIndex(response[:open], "{")+1:open], "%d", &size); err != nil {
		t.Fatalf("literal size in %q: %v", response, err)
	}
	return response[open+3 : open+3+size]
}

// TestRFC2231ParameterReproducerThroughTheGateway is #1886's report, sent through the production
// delivery route into PostgreSQL and read back by a client of the REST Mail IMAP gateway. An RFC 2231
// parameter in a charset other than UTF-8 or US-ASCII used to vanish from BODYSTRUCTURE, and from a
// header section that rewrote its field for another, 8-bit parameter. Every parameter has to reach
// the client, the ones beside it unchanged, and the stored octets and BINARY have to stay as sent.
func TestRFC2231ParameterReproducerThroughTheGateway(t *testing.T) {
	x := newRFC2231Fixture(t)

	t.Run("BODYSTRUCTURE keeps NAME, FILENAME and the parameter beside them", func(t *testing.T) {
		const message = repeatedHead +
			"Content-Type: text/plain; charset=utf-8; name*=windows-1252''J%F6rg.txt; other=keep\r\n" +
			"Content-Disposition: attachment; filename*=iso-8859-2''%A3.txt\r\n\r\nbody\r\n"
		x.deliver(t, message)
		x.imap.do("SELECT INBOX")
		structure := x.imap.do("FETCH 1 (BODYSTRUCTURE)")
		// Before the fix this was ("CHARSET" "utf-8" "OTHER" "keep") ... ("ATTACHMENT" NIL).
		for _, want := range []string{
			`("TEXT" "PLAIN" ("CHARSET" "utf-8" "NAME" "=?windows-1252?Q?J=F6rg=2Etxt?=" "OTHER" "keep")`,
			`("ATTACHMENT" ("FILENAME" "=?iso-8859-2?Q?=A3=2Etxt?="))`,
		} {
			if !strings.Contains(structure, want) {
				t.Errorf("BODYSTRUCTURE %s\n  want %s in it", structure, want)
			}
		}
	})

	t.Run("a rewritten header section keeps the RFC 2231 parameter as sent", func(t *testing.T) {
		const field = "Content-Type: application/x-y; name*=windows-1252''x%F6.bin; note=\"Gr\xc3\xbc\""
		message := repeatedHead + field + "\r\n\r\nbody\r\n"
		stored := x.deliver(t, message)
		x.imap.do("SELECT INBOX")
		header := literal(t, x.imap.do("FETCH 1 (BODY.PEEK[HEADER])"))
		// Before the fix the field went out as "Content-Type: application/x-y;\r\n note*=UTF-8''Gr%C3%BC".
		const want = "Content-Type: application/x-y;\r\n name*=windows-1252''x%F6.bin;\r\n note*=UTF-8''Gr%C3%BC\r\n"
		if !strings.Contains(header, want) {
			t.Errorf("HEADER %q\n  want %q in it", header, want)
		}
		structure := x.imap.do("FETCH 1 (BODYSTRUCTURE)")
		if want := `("NAME" "=?windows-1252?Q?x=F6=2Ebin?=" "NOTE" "=?UTF-8?Q?Gr=C3=BC?=")`; !strings.Contains(structure, want) {
			t.Errorf("BODYSTRUCTURE %s\n  want %s in it", structure, want)
		}
		// "The stored octets don't change, and BINARY isn't affected." This session hasn't enabled
		// UTF-8, so the gateway sends the header it downgraded (RFC 6855 §3) through BINARY too, and the
		// RFC 2231 parameter has to be in it as sent.
		if binary := literal(t, x.imap.do("FETCH 1 (BINARY.PEEK[])")); !strings.Contains(binary, want) || !strings.HasSuffix(binary, "\r\n\r\nbody\r\n") {
			t.Errorf("BINARY[] %q\n  want %q in it", binary, want)
		}
		// After UTF8=ACCEPT nothing is downgraded: HEADER and BINARY[] are the stored octets.
		utf8 := openIMAPSession(t, x.f, x.f.gatewayClient(x.srv.URL, imapCN))
		for _, command := range []string{"CLOSE", "ENABLE UTF8=ACCEPT", "SELECT INBOX"} {
			if answer := utf8.do(command); !strings.Contains(answer, " OK") {
				t.Fatalf("%s: %s", command, answer)
			}
		}
		if header := literal(t, utf8.do("FETCH 1 (BODY.PEEK[HEADER])")); !strings.Contains(header, "\r\n"+field+"\r\n") {
			t.Errorf("UTF-8 HEADER %q\n  want %q in it", header, field)
		}
		if binary := literal(t, utf8.do("FETCH 1 (BINARY.PEEK[])")); binary != string(stored.Source()) {
			t.Errorf("UTF-8 BINARY[] %q\n  stored %q", binary, stored.Source())
		}
		if again := lastStored(t, x.f, message); string(again.Source()) != string(stored.Source()) {
			t.Errorf("stored source changed after FETCH")
		}
	})
}

// TestRFC2231FilenamesReadTheSameEverywhere: an attachment's RFC 2231 filename, in any charset this
// server has a reader for, continued or not, with or without a language, is the same text in the
// attachment row, the REST API's attachment list and what the filters are handed, and goes to an IMAP
// client in BODYSTRUCTURE with the parameters beside it. A charset no one here reads keeps its octets
// in an encoded word naming it, so nothing is guessed and nothing is dropped. The expected names are
// the charsets' own code points, not this server's output.
func TestRFC2231FilenamesReadTheSameEverywhere(t *testing.T) {
	x := newRFC2231Fixture(t)
	for _, row := range []struct {
		name, fields string
		// filename is what the row, the list and the filters hold; structure is a piece of BODYSTRUCTURE.
		filename, structure string
	}{
		{"#1886's fields on a part: the disposition's filename names it, the type's name stays",
			"Content-Type: application/octet-stream; name*=windows-1252''J%F6rg.txt; other=keep\r\nContent-Disposition: attachment; filename*=iso-8859-2''%A3.txt",
			"Ł.txt", `"OCTET-STREAM" ("NAME" "=?windows-1252?Q?J=F6rg=2Etxt?=" "OTHER" "keep") NIL NIL "7BIT" 5 NIL ("ATTACHMENT" ("FILENAME" "=?iso-8859-2?Q?=A3=2Etxt?="))`},
		{"a type's name in Windows-1252, beside another parameter",
			"Content-Type: application/octet-stream; name*=windows-1252''J%F6rg%80.bin; other=keep\r\nContent-Disposition: attachment",
			"Jörg€.bin", `("NAME" "=?windows-1252?Q?J=F6rg=80=2Ebin?=" "OTHER" "keep")`},
		{"Latin-1", "Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename*=iso-8859-1''caf%E9.txt; size=5",
			"café.txt", `("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?caf=C3=A9=2Etxt?=" "SIZE" "5"))`},
		{"Latin-2", "Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename*=iso-8859-2''%A3%F3d%BC.txt; note=keep",
			"Łódź.txt", `("ATTACHMENT" ("FILENAME" "=?iso-8859-2?Q?=A3=F3d=BC=2Etxt?=" "NOTE" "keep"))`},
		{"KOI8-R", "Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename*=koi8-r''%F0%D2%C9%D7%C5%D4.txt",
			"Привет.txt", `("ATTACHMENT" ("FILENAME" "=?koi8-r?Q?=F0=D2=C9=D7=C5=D4=2Etxt?="))`},
		{"Shift_JIS", "Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename*=Shift_JIS''%93%FA%96%7B.txt",
			"日本.txt", `("ATTACHMENT" ("FILENAME" "=?Shift_JIS?Q?=93=FA=96=7B=2Etxt?="))`},
		{"UTF-8", "Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename*=utf-8''J%C3%B6rg.txt",
			"Jörg.txt", `("ATTACHMENT" ("FILENAME" "=?UTF-8?Q?J=C3=B6rg=2Etxt?="))`},
		{"a language tag", "Content-Type: application/octet-stream\r\nContent-Disposition: inline; filename*=WINDOWS-1252'de'%F6.txt",
			"ö.txt", `("INLINE" ("FILENAME" "=?WINDOWS-1252?Q?=F6=2Etxt?="))`},
		{"continued segments, encoded and plain, in Windows-1252",
			"Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename*0*=windows-1252''J%F6; filename*1=\"rg \"; filename*2*=%F6.txt; after=keep",
			"Jörg ö.txt", `("ATTACHMENT" ("AFTER" "keep" "FILENAME" "=?windows-1252?Q?J=F6rg_=F6=2Etxt?="))`},
		{"continued segments over folded lines, in Latin-2",
			"Content-Type: application/octet-stream;\r\n name*0*=iso-8859-2''%A3%F3;\r\n name*1*=d%BC.bin;\r\n other=keep\r\nContent-Disposition: attachment",
			"Łódź.bin", `("NAME" "=?iso-8859-2?Q?=A3=F3d=BC=2Ebin?=" "OTHER" "keep")`},
		{"a charset no one here reads keeps its octets in a word naming it",
			"Content-Type: application/octet-stream\r\nContent-Disposition: attachment; filename*=x-no-such-charset''J%F6rg.txt; note=keep",
			"=?x-no-such-charset?Q?J=F6rg=2Etxt?=", `("ATTACHMENT" ("FILENAME" "=?x-no-such-charset?Q?J=F6rg=2Etxt?=" "NOTE" "keep"))`},
	} {
		t.Run(row.name, func(t *testing.T) {
			// A stored part is shared by its content, and a shared one keeps its first name.
			content := fmt.Sprintf("%x", row.name)[:5]
			message := repeatedHead + "Content-Type: multipart/mixed; boundary=a\r\n\r\n" +
				"--a\r\nContent-Type: text/plain\r\n\r\nvisible text\r\n" +
				"--a\r\n" + row.fields + "\r\n\r\n" + content + "\r\n" +
				"--a\r\nContent-Type: text/plain\r\n\r\nafter the part\r\n--a--\r\n"
			stored := x.deliver(t, message)

			var rows []models.Attachment
			if err := x.f.db.Where("message_id = ?", stored.ID).Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Filename != row.filename {
				t.Errorf("attachment rows %+v, want one named %q", rows, row.filename)
			}

			answer := x.f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/attachments", stored.ID), x.f.session, "")
			var listed struct {
				Data []struct {
					Filename string `json:"filename"`
				} `json:"data"`
			}
			if err := json.Unmarshal(answer.Body.Bytes(), &listed); err != nil || answer.Code != http.StatusOK || len(listed.Data) != 1 {
				t.Fatalf("listAttachments %d %.300s (%v)", answer.Code, answer.Body, err)
			}
			if got := listed.Data[0].Filename; got != row.filename {
				t.Errorf("listAttachments filename %q, want %q", got, row.filename)
			}

			x.seen.mu.Lock()
			var handed []string
			for _, attachment := range x.seen.email.Attachments {
				handed = append(handed, attachment.Filename)
			}
			x.seen.mu.Unlock()
			if len(handed) != 1 || handed[0] != row.filename {
				t.Errorf("filters were handed %q, want [%q]", handed, row.filename)
			}

			x.imap.do("SELECT INBOX")
			structure := x.imap.do("FETCH 1 (BODYSTRUCTURE)")
			if !strings.Contains(structure, row.structure) {
				t.Errorf("BODYSTRUCTURE %s\n  want %s in it", structure, row.structure)
			}
			// The parts on either side are still there, whole.
			for section, want := range map[string]string{"1": "visible text", "2": content, "3": "after the part"} {
				if got := x.imap.do("FETCH 1 (BODY.PEEK[" + section + "])"); !strings.Contains(got, want) {
					t.Errorf("BODY[%s] %s, want %q in it", section, got, want)
				}
			}
		})
	}
}
