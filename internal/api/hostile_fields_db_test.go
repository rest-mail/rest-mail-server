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
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	pop3gw "github.com/rest-mail/rest-mail-server/internal/gateway/pop3"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	pipelinefilters "github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

// senderRoute delivers a message down one path a sender can reach, and says why it was refused.
type senderRoute struct {
	name string
	send func(t *testing.T, message string) error
}

// senderRoutes are the paths mail arrives by, through the real router, with the inbound pipeline a
// new domain gets: attachments are stored, which is where a part's fields reach a row.
func senderRoutes(t *testing.T) (*credentialFixture, []senderRoute) {
	t.Helper()
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize})
	var domain models.Domain
	if err := f.db.First(&domain, "name = ?", "example.test").Error; err != nil {
		t.Fatal(err)
	}
	filters := fmt.Sprintf(`[{"name":"extract_attachments","type":"transform","enabled":true,"config":{"storage_path":%q}},{"name":"sieve","type":"transform","enabled":true}]`, t.TempDir())
	if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: json.RawMessage(filters), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	_ = free.Close()
	gateway := smtpgw.NewServer("mail.example.test", apiclient.New(srv.URL), nil,
		smtpgw.NewStore(f.db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 100}))
	if err := gateway.ListenAndServe(smtpgw.SMTPPorts{Inbound: port}); err != nil {
		t.Fatalf("start the SMTP gateway: %v", err)
	}
	t.Cleanup(gateway.Shutdown)
	viaSMTP := func(t *testing.T, message string) error {
		client, err := smtp.Dial(free.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = client.Close() }()
		if err := client.Hello("client.test"); err != nil {
			t.Fatal(err)
		}
		return send(client, "sender@remote.test", f.address, message)
	}
	viaDeliver := func(t *testing.T, message string) error {
		// The gateway sends the Message-ID it read. The rest of what it reads, the server reads too.
		messageID := ""
		if _, rest, found := strings.Cut(message, "Message-ID: "); found {
			messageID, _, _ = strings.Cut(rest, ">")
			messageID = strings.TrimPrefix(strings.ReplaceAll(messageID, "\r\n ", " "), "<")
		}
		body, err := json.Marshal(map[string]any{"address": f.address, "sender": "sender@remote.test", "recipients_to": []string{f.address}, "message_id": messageID,
			"client_ip": "192.0.2.1", "helo_name": "mail.remote.test", "received_tls": true, "raw_message": base64.StdEncoding.EncodeToString([]byte(message))})
		if err != nil {
			t.Fatal(err)
		}
		if answer := f.do(http.MethodPost, "/api/v1/messages/deliver", "", string(body)); answer.Code != http.StatusCreated {
			return fmt.Errorf("%d %s", answer.Code, answer.Body)
		}
		return nil
	}
	viaRestmail := func(t *testing.T, message string) error {
		body, err := json.Marshal(map[string]any{"from": "sender@remote.test", "to": []string{f.address}, "raw_message": message})
		if err != nil {
			t.Fatal(err)
		}
		answer := f.do(http.MethodPost, "/restmail/messages", "", string(body))
		if answer.Code != http.StatusCreated || !strings.Contains(answer.Body.String(), `"failed":[]`) {
			return fmt.Errorf("%d %s", answer.Code, answer.Body)
		}
		return nil
	}
	return f, []senderRoute{{"SMTP DATA", viaSMTP}, {"deliver", viaDeliver}, {"RESTMAIL", viaRestmail}}
}

// lastStored is the newest message in the fixture's mailbox, checked to be what arrived: exact in
// its source, and sealed over what the row holds.
func lastStored(t *testing.T, f *credentialFixture, message string) models.Message {
	t.Helper()
	var stored models.Message
	if err := f.db.Where("mailbox_id = ?", f.mailboxID).Order("id DESC").First(&stored).Error; err != nil {
		t.Fatalf("not stored: %v", err)
	}
	// The gateway puts its Received field in front.
	if !strings.HasSuffix(string(stored.Source()), message) {
		t.Fatalf("source of %d bytes, sent %d: %.120q", len(stored.Source()), len(message), stored.Source())
	}
	digest, err := stored.SealDigest()
	if err != nil || digest != stored.SealedHash {
		t.Fatalf("seal %q, digest of what is stored %q (%v)", stored.SealedHash, digest, err)
	}
	return stored
}

// A sender writes a part's fields and a message's identifiers, at any length and in any octets. None
// of it may refuse the message, on any path: the row holds what fits and the source what arrived.
func TestSenderFieldsDoNotRefuseMail(t *testing.T) {
	f, routes := senderRoutes(t)
	const head = "From: Old <old@remote.test>\r\nTo: chris@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	part := func(fields string) string {
		return head + "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nhello\r\n--B\r\n" + fields + "\r\n\r\nPDF\r\n--B--\r\n"
	}
	continued := func(parameter string, segments, each int, last string) string {
		var b strings.Builder
		for i := range segments {
			fmt.Fprintf(&b, ";\r\n %s*%d=\"%s\"", parameter, i, strings.Repeat("x", each))
		}
		fmt.Fprintf(&b, ";\r\n %s*%d=\"%s\"", parameter, segments, last)
		return b.String()
	}
	const pdf = "Content-Type: application/pdf"
	withoutHead := func(fields string) string {
		return "To: chris@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n" + fields + "\r\n\r\nb\r\n"
	}
	for _, tc := range []struct {
		name, message string
		// filename and mediaType are what the attachment's row answers; "" means there is none.
		filename, mediaType string
		check               func(t *testing.T, stored models.Message)
	}{
		{name: "name*N, a little over the column", message: part(pdf + continued("name", 10, 30, ".pdf")),
			filename: strings.Repeat("x", 251) + ".pdf", mediaType: "application/pdf"},
		{name: "name*N, most of a megabyte", message: part(pdf + continued("name", 15000, 48, ".pdf")),
			filename: strings.Repeat("x", 251) + ".pdf", mediaType: "application/pdf"},
		{name: "filename*N, a little over the column", message: part(pdf + "\r\nContent-Disposition: attachment" + continued("filename", 10, 30, ".exe")),
			filename: strings.Repeat("x", 251) + ".exe", mediaType: "application/pdf"},
		{name: "filename exactly the column", message: part(pdf + "\r\nContent-Disposition: attachment" + continued("filename", 8, 30, strings.Repeat("y", 15))),
			filename: strings.Repeat("x", 240) + strings.Repeat("y", 15), mediaType: "application/pdf"},
		{name: "many characters, few of them", message: part(pdf + "\r\nContent-Disposition: attachment; filename*=utf-8''" + strings.Repeat("%E6%97%A5", 200) + ".pdf"),
			filename: strings.Repeat("日", 200) + ".pdf", mediaType: "application/pdf"},
		{name: "a NUL in the filename", message: part(pdf + "\r\nContent-Disposition: attachment; filename*=utf-8''a%00b.pdf"),
			filename: "a b.pdf", mediaType: "application/pdf"},
		{name: "invalid UTF-8 in the filename", message: part(pdf + "\r\nContent-Disposition: attachment; filename*=utf-8''a%FFb.pdf"),
			filename: "a�b.pdf", mediaType: "application/pdf"},
		{name: "a character across the storage path's cut", message: part(pdf + "\r\nContent-Disposition: attachment; filename*=utf-8''" + strings.Repeat("y", 99) + "%C3%A9.pdf"),
			filename: strings.Repeat("y", 99) + "é.pdf", mediaType: "application/pdf"},
		{name: "a media type past RFC 6838's", message: part("Content-Type: application/" + strings.Repeat("z", 300) + "\r\nContent-Disposition: attachment; filename=a.bin"),
			filename: "a.bin", mediaType: "application/octet-stream"},
		{name: "a media type at RFC 6838's", message: part("Content-Type: application/" + strings.Repeat("z", 127) + "\r\nContent-Disposition: attachment; filename=a.bin"),
			filename: "a.bin", mediaType: "application/" + strings.Repeat("z", 127)},
		{name: "a long sender name", message: withoutHead("From: " + strings.Repeat("N", 300) + " <old@remote.test>")},
		{name: "a long sender address", message: withoutHead("From: " + strings.Repeat("a", 300) + "@remote.test")},
		{name: "a long Message-ID", message: withoutHead("From: old@remote.test\r\nMessage-ID: <" + strings.Repeat("m", 900) + "\r\n " + strings.Repeat("m", 300) + "@remote.test>"),
			check: func(t *testing.T, stored models.Message) {
				if len(stored.MsgID) > 995 || stored.MsgID == "" {
					t.Fatalf("message_id %d long", len(stored.MsgID))
				}
			}},
		{name: "a long In-Reply-To", message: withoutHead("From: old@remote.test\r\nIn-Reply-To: <" + strings.Repeat("r", 900) + "\r\n " + strings.Repeat("r", 300) + "@x.test>"),
			check: func(t *testing.T, stored models.Message) {
				if stored.InReplyTo != "" {
					t.Fatalf("in_reply_to %d long", len(stored.InReplyTo))
				}
			}},
		{name: "a long first reference", message: withoutHead("From: old@remote.test\r\nReferences: <" + strings.Repeat("r", 1200) + "@x.test>")},
	} {
		for _, route := range routes {
			t.Run(tc.name+"/"+route.name, func(t *testing.T) {
				message := fmt.Sprintf("Message-ID: <%x@remote.test>\r\n", tc.name+route.name) + tc.message
				if strings.Contains(tc.message, "Message-ID:") {
					message = tc.message
				}
				// Stored parts are shared by their content, and a shared one keeps its first name.
				message = strings.Replace(message, "PDF\r\n", fmt.Sprintf("PDF %x\r\n", tc.name+route.name), 1)
				if err := route.send(t, message); err != nil {
					t.Fatalf("refused: %v", err)
				}
				stored := lastStored(t, f, message)
				if tc.check != nil {
					tc.check(t, stored)
				}
				// RESTMAIL keeps no attachment rows: its parts stay in the source.
				if tc.filename == "" || route.name == "RESTMAIL" {
					return
				}
				answer := f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/attachments", stored.ID), f.session, "")
				var listed struct {
					Data []struct {
						ID          uint   `json:"id"`
						Filename    string `json:"filename"`
						ContentType string `json:"content_type"`
					} `json:"data"`
				}
				if err := json.Unmarshal(answer.Body.Bytes(), &listed); err != nil || len(listed.Data) != 1 {
					t.Fatalf("attachments %d %.200s (%v)", answer.Code, answer.Body, err)
				}
				if got := listed.Data[0]; got.Filename != tc.filename || got.ContentType != tc.mediaType {
					t.Fatalf("attachment %q %q, want %q %q", got.Filename, got.ContentType, tc.filename, tc.mediaType)
				}
			})
		}
	}
}

// A long field beside a signal must not hide the signal: not from the class on read, which on these
// rows comes from the source; not from a Sieve rule, which runs on the parse; not from threading.
func TestLongHeaderFieldHidesNoSignal(t *testing.T) {
	f, routes := senderRoutes(t)
	const script = `require ["fileinto"];
if header :contains "X-Route-Me" "yes" { fileinto "Routed"; }`
	if err := f.db.AutoMigrate(&models.SieveScript{}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.SieveScript{MailboxID: f.mailboxID, Script: script, ScriptHash: pipelinefilters.HashSieveScript(script), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	long := func(size int) string {
		return "X-Long: " + strings.Repeat("yyyyyyy\r\n ", size/10) + "y\r\n"
	}
	const fixed = "From: old@remote.test\r\nTo: chris@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	sizes := []struct {
		name string
		size int
	}{{"under the column rule", 60 << 10}, {"over the column rule", 90 << 10}, {"most of 1 MiB", 900 << 10}, {"past 1 MiB", 1200 << 10}}
	read := func(t *testing.T, id uint) (answer struct {
		Class    string          `json:"class"`
		Headers  json.RawMessage `json:"headers"`
		ThreadID string          `json:"thread_id"`
		Folder   string          `json:"folder"`
	}) {
		t.Helper()
		w := f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", id), f.session, "")
		var body struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
			t.Fatalf("read %d %.200s (%v)", w.Code, w.Body, err)
		}
		if err := json.Unmarshal(body.Data, &answer); err != nil {
			t.Fatal(err)
		}
		return answer
	}
	for _, route := range routes {
		for _, size := range sizes {
			for _, signal := range []struct{ field, class string }{
				{"List-Id: News <news.remote.test>", "newsletter"},
				{"List-Unsubscribe: <https://remote.test/leave>", "newsletter"},
				{"Auto-Submitted: auto-generated", "notification"},
				{"Precedence: bulk", "notification"},
				{"X-Plain: nothing", "person"},
			} {
				for _, order := range []string{"long first", "long last"} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", route.name, size.name, signal.field, order), func(t *testing.T) {
						fields := long(size.size) + signal.field + "\r\n"
						if order == "long last" {
							fields = signal.field + "\r\n" + long(size.size)
						}
						message := fmt.Sprintf("Message-ID: <%x@remote.test>\r\n", t.Name()) + fixed + fields + "\r\nb\r\n"
						if err := route.send(t, message); err != nil {
							t.Fatalf("refused: %v", err)
						}
						got := read(t, lastStored(t, f, message).ID)
						if got.Class != signal.class {
							t.Fatalf("class %s, want %s", got.Class, signal.class)
						}
					})
				}
			}
			t.Run(fmt.Sprintf("%s/%s/threads with its parent", route.name, size.name), func(t *testing.T) {
				parentID := fmt.Sprintf("%x@remote.test", t.Name())
				parent := "Message-ID: <" + parentID + ">\r\n" + fixed + "\r\nb\r\n"
				if err := route.send(t, parent); err != nil {
					t.Fatalf("refused: %v", err)
				}
				thread := read(t, lastStored(t, f, parent).ID).ThreadID
				for _, reply := range []string{"In-Reply-To: <" + parentID + ">", "References: <" + parentID + ">"} {
					for _, order := range []string{"long first", "long last"} {
						fields := long(size.size) + reply + "\r\n"
						if order == "long last" {
							fields = reply + "\r\n" + long(size.size)
						}
						message := fmt.Sprintf("Message-ID: <%x@remote.test>\r\n", reply+order+thread) + fixed + fields + "\r\nb\r\n"
						if err := route.send(t, message); err != nil {
							t.Fatalf("refused: %v", err)
						}
						if got := read(t, lastStored(t, f, message).ID).ThreadID; got != thread {
							t.Fatalf("%s, %s: thread %.60s, want %.60s", reply, order, got, thread)
						}
					}
				}
			})
			t.Run(fmt.Sprintf("%s/%s/a Sieve rule sees the field", route.name, size.name), func(t *testing.T) {
				for _, order := range []string{"long first", "long last"} {
					fields := long(size.size) + "X-Route-Me: yes\r\n"
					if order == "long last" {
						fields = "X-Route-Me: yes\r\n" + long(size.size)
					}
					message := fmt.Sprintf("Message-ID: <%x@remote.test>\r\n", t.Name()+order) + fixed + fields + "\r\nb\r\n"
					if err := route.send(t, message); err != nil {
						t.Fatalf("refused: %v", err)
					}
					if got := read(t, lastStored(t, f, message).ID).Folder; got != "Routed" {
						t.Fatalf("%s: folder %s, want Routed", order, got)
					}
				}
			})
		}
	}
}

// A row whose long field stayed in the source, and everything that reads one. headers is left out
// where the row kept none, and whatever needs a field reads it from the source.
func TestRowWithALongFieldServesEveryReader(t *testing.T) {
	f, routes := senderRoutes(t)
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	api := apiclient.New(srv.URL)
	imapMailbox, err := imapgw.NewBackend(api, nil).Authenticate(f.address, f.password)
	if err != nil {
		t.Fatal(err)
	}
	pop3Mailbox, err := pop3gw.NewBackend(api, nil).Authenticate(f.address, f.password)
	if err != nil {
		t.Fatal(err)
	}
	const signal = "List-Id: News <news.remote.test>"
	for _, route := range routes {
		for _, size := range []struct {
			name string
			size int
			// keepsHeaders says the row holds decoded headers: only RESTMAIL's, under the rule.
			keepsHeaders bool
		}{{"under the column rule", 60 << 10, route.name == "RESTMAIL"}, {"over the column rule", 90 << 10, false}, {"past 1 MiB", 1200 << 10, false}} {
			t.Run(route.name+"/"+size.name, func(t *testing.T) {
				if err := f.db.Where("mailbox_id = ?", f.mailboxID).Delete(&models.Message{}).Error; err != nil {
					t.Fatal(err)
				}
				message := fmt.Sprintf("Message-ID: <%x@remote.test>\r\n", t.Name()) +
					"From: old@remote.test\r\nTo: chris@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\nX-Long: " + strings.Repeat("yyyyyyy\r\n ", size.size/10) + "y\r\n" + signal + "\r\n" +
					"Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain\r\n\r\nhello\r\n--B\r\nContent-Type: application/pdf; name=a.pdf\r\n\r\nPDF\r\n--B--\r\n"
				if err := route.send(t, message); err != nil {
					t.Fatalf("refused: %v", err)
				}
				stored := lastStored(t, f, message)

				// The class, on all three ways the API answers a message.
				var single struct {
					Data struct {
						Class   string          `json:"class"`
						Headers json.RawMessage `json:"headers"`
					} `json:"data"`
				}
				if err := json.Unmarshal(f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", stored.ID), f.session, "").Body.Bytes(), &single); err != nil {
					t.Fatal(err)
				}
				if single.Data.Class != "newsletter" {
					t.Fatalf("single: class %s", single.Data.Class)
				}
				// A row with none answers an empty list, as every /deliver row always has.
				if kept := string(single.Data.Headers) != "[]"; kept != size.keepsHeaders {
					t.Fatalf("headers %.40s, kept: %v", single.Data.Headers, size.keepsHeaders)
				}
				for _, path := range []string{
					fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages", f.mailboxID),
					fmt.Sprintf("/api/v1/accounts/%d/search?q=hello", f.mailboxID),
				} {
					var many struct {
						Data []struct {
							ID    uint   `json:"id"`
							Class string `json:"class"`
						} `json:"data"`
					}
					answer := f.do(http.MethodGet, path, f.session, "")
					if err := json.Unmarshal(answer.Body.Bytes(), &many); err != nil {
						t.Fatal(err)
					}
					if len(many.Data) != 1 || many.Data[0].ID != stored.ID || many.Data[0].Class != "newsletter" {
						t.Fatalf("%s: %d %.200s", path, answer.Code, answer.Body)
					}
				}

				// The source, exact, for the raw route.
				raw := f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/raw", stored.ID), f.session, "")
				if raw.Code != http.StatusOK || !strings.HasSuffix(raw.Body.String(), message) {
					t.Fatalf("raw: %d, %d bytes", raw.Code, raw.Body.Len())
				}

				// The protocol gateways, which read the source and not the column.
				listed, err := imapMailbox.Messages("INBOX")
				if err != nil || len(listed) != 1 {
					t.Fatalf("IMAP: %d messages (%v)", len(listed), err)
				}
				fetched, err := imapMailbox.Fetch(listed[0].UID)
				if err != nil || !strings.Contains(string(fetched), signal) {
					t.Fatalf("IMAP FETCH: %d bytes (%v)", len(fetched), err)
				}
				popped, err := pop3Mailbox.Messages()
				if err != nil || len(popped) == 0 {
					t.Fatalf("POP3: %d messages (%v)", len(popped), err)
				}
				retrieved, err := pop3Mailbox.Retrieve(popped[len(popped)-1].UID)
				if err != nil || !strings.Contains(string(retrieved), signal) {
					t.Fatalf("POP3 RETR: %d bytes (%v)", len(retrieved), err)
				}

				// A filter that needs the parts still gets them: RESTMAIL keeps no attachment rows.
				if route.name != "RESTMAIL" {
					var attachment models.Attachment
					if err := f.db.Where("message_id = ?", stored.ID).First(&attachment).Error; err != nil || attachment.Filename != "a.pdf" {
						t.Fatalf("attachment %q (%v)", attachment.Filename, err)
					}
				}
			})
		}
	}
}

// What a field that ends the source's reading does to the class must not depend on whether a long
// field put the row on the source: a short message and a long one get the same answer.
func TestLongFieldDoesNotChangeAMalformedFieldsClass(t *testing.T) {
	f, routes := senderRoutes(t)
	class := func(t *testing.T, route senderRoute, message string) string {
		t.Helper()
		if err := route.send(t, message); err != nil {
			t.Fatalf("refused: %v", err)
		}
		var single struct {
			Data struct {
				Class string `json:"class"`
			} `json:"data"`
		}
		if err := json.Unmarshal(f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", lastStored(t, f, message).ID), f.session, "").Body.Bytes(), &single); err != nil {
			t.Fatal(err)
		}
		return single.Data.Class
	}
	const fixed = "From: old@remote.test\r\nTo: chris@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	long := "X-Long: " + strings.Repeat("yyyyyyy\r\n ", 9_000) + "y\r\n"
	for _, route := range routes {
		for _, bad := range []string{"Bad Field: x\r\n", "X-\xc3\xa9: x\r\n", ": x\r\n", "NoColon\r\n"} {
			t.Run(fmt.Sprintf("%s/%q", route.name, bad), func(t *testing.T) {
				body := "List-Id: News <news.remote.test>\r\n\r\nb\r\n"
				short := class(t, route, fmt.Sprintf("Message-ID: <%x@remote.test>\r\n", t.Name()+"s")+fixed+bad+body)
				withLong := class(t, route, fmt.Sprintf("Message-ID: <%x@remote.test>\r\n", t.Name()+"l")+fixed+long+bad+body)
				t.Logf("short %s, long %s", short, withLong)
				if short != withLong {
					t.Fatalf("short %s, long %s", short, withLong)
				}
			})
		}
	}
}

// A peer writes RESTMAIL's headers beside the source, so a list's mail could claim to be a person's
// by sending them: what's kept is what the source says.
func TestRestmailHeadersDoNotDecideTheClass(t *testing.T) {
	f, _ := senderRoutes(t)
	message := "Message-ID: <claim@remote.test>\r\nFrom: old@remote.test\r\nTo: chris@example.test\r\nList-Id: News <news.remote.test>\r\n\r\nb\r\n"
	body, err := json.Marshal(map[string]any{"from": "old@remote.test", "to": []string{f.address}, "raw_message": message, "headers": map[string][]string{"X-Plain": {"nothing"}}})
	if err != nil {
		t.Fatal(err)
	}
	if answer := f.do(http.MethodPost, "/restmail/messages", "", string(body)); answer.Code != http.StatusCreated {
		t.Fatalf("%d %s", answer.Code, answer.Body)
	}
	var single struct {
		Data struct {
			Class string `json:"class"`
		} `json:"data"`
	}
	if err := json.Unmarshal(f.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", lastStored(t, f, message).ID), f.session, "").Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if single.Data.Class != "newsletter" {
		t.Fatalf("class %s", single.Data.Class)
	}
	if stored := lastStored(t, f, message); strings.Contains(string(stored.Headers), "X-Plain") {
		t.Fatalf("stored the peer's headers: %s", stored.Headers)
	}
}
