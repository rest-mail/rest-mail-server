package api

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"
	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	pop3gw "github.com/rest-mail/rest-mail-server/internal/gateway/pop3"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// eightBitShape is a message as a real sender writes it.
type eightBitShape struct {
	name    string
	message string
}

func eightBitShapes() []eightBitShape {
	headers := func(extra string) string {
		return "From: Sender <sender@remote.test>\r\nTo: chris@example.test\r\nSubject: shape\r\nDate: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <" +
			strconv.FormatInt(time.Now().UnixNano(), 36) + "@remote.test>\r\nMIME-Version: 1.0\r\n" + extra + "\r\n"
	}
	binary := make([]byte, 0, 2048)
	for i := 0; i < 2048; i++ {
		if b := byte(i%255 + 1); b != '\r' && b != '\n' {
			binary = append(binary, b)
		}
		if i%64 == 63 {
			binary = append(binary, '\r', '\n')
		}
	}
	calendar := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:caf\xe9@remote.test\r\nDTSTART:20261105T100000Z\r\nDTEND:20261105T110000Z\r\nSUMMARY:R\xe9union\x00\r\nORGANIZER:mailto:sender@remote.test\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	return []eightBitShape{
		{"control ASCII", headers("Content-Type: text/plain\r\n") + "plain\r\n"},
		{"Latin-1 declared", headers("Content-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n") + "Caf\xe9 cr\xe8me \xfcber na\xefve\r\n"},
		{"8-bit no charset", headers("Content-Type: text/plain\r\nContent-Transfer-Encoding: 8bit\r\n") + "Stra\xdfe \xa3100\r\n"},
		{"8-bit no MIME headers", "From: sender@remote.test\r\nTo: chris@example.test\r\nSubject: old\r\nDate: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <old-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@remote.test>\r\n\r\nGr\xfc\xdfe\r\n"},
		{"8-bit HTML", headers("Content-Type: text/html; charset=windows-1252\r\nContent-Transfer-Encoding: 8bit\r\n") + "<p>caf\xe9 \x93quoted\x94</p>\r\n"},
		{"binary attachment", headers("Content-Type: multipart/mixed; boundary=\"b1\"\r\n") + "--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nsee attached\r\n--b1\r\nContent-Type: application/octet-stream; name=\"a.bin\"\r\nContent-Disposition: attachment; filename=\"a.bin\"\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + string(binary) + "\r\n--b1--\r\n"},
		{"8-bit filename", headers("Content-Type: multipart/mixed; boundary=\"b1\"\r\n") + "--b1\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n--b1\r\nContent-Type: application/pdf; name=\"caf\xe9.a.bin\"\r\nContent-Disposition: attachment; filename=\"caf\xe9.a.bin\"\r\nContent-Transfer-Encoding: 8bit\r\n\r\n\xe9\xe9\r\n--b1--\r\n"},
		{"ASCII calendar", headers("Content-Type: text/calendar; method=REQUEST\r\n") + strings.NewReplacer("\xe9", "e", "\x00", "").Replace(calendar)},
		{"8-bit calendar", headers("Content-Type: text/calendar; method=REQUEST\r\nContent-Transfer-Encoding: 8bit\r\n") + calendar},
		{"NUL in text body", headers("Content-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n") + "before\x00after \xe9\r\n"},
		{"NUL in UTF-8 body", headers("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n") + "before\x00after\r\n"},
		{"NUL in attachment", headers("Content-Type: application/octet-stream\r\nContent-Transfer-Encoding: 8bit\r\n") + "before\x00after \xe9\r\n"},
		{"NUL in Subject", strings.Replace(headers("Content-Type: text/plain\r\n"), "Subject: shape", "Subject: be\x00fore", 1) + "body\r\n"},
		{"many parameters on an 8-bit part", headers("Content-Type: multipart/mixed; boundary=B\r\n") + "--B\r\nContent-Type: text/plain; name=\"caf\xe9\"" + manyParameters(64<<10) + "\r\n\r\nb\xe9\r\n--B--\r\n"},
	}
}

func manyParameters(size int) string {
	var parameters strings.Builder
	for i := 0; parameters.Len() < size; i++ {
		fmt.Fprintf(&parameters, ";\r\n p%d=v%d", i, i)
	}
	return parameters.String()
}

// eightBitServer is a real API on Postgres with the SMTP gateway's inbound port in front of it,
// recording how the API answered each delivery.
type eightBitServer struct {
	*credentialFixture
	api     *apiclient.Client
	addr    string
	subAddr string
	rootCAs *x509.CertPool

	mu     sync.Mutex
	status []int
	bodies []string
}

func newEightBitServer(t *testing.T) *eightBitServer {
	t.Helper()
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", SMTPMaxMessageSize: config.DefaultSMTPMaxMessageSize})
	serveTXT(t, map[string][]string{})
	// The default inbound pipeline, keeping attachments where this test may write them.
	var filters []pipeline.FilterConfig
	if err := json.Unmarshal(pipeline.DefaultPipelineJSON("inbound"), &filters); err != nil {
		t.Fatal(err)
	}
	for i := range filters {
		if filters[i].Name == "extract_attachments" {
			filters[i].Config, _ = json.Marshal(map[string]string{"storage_path": t.TempDir()})
		}
	}
	encoded, _ := json.Marshal(filters)
	var domain models.Domain
	if err := f.db.Where("name = ?", "example.test").First(&domain).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: encoded, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	s := &eightBitServer{credentialFixture: f}
	if err := f.db.Create(&models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, r)
		if r.URL.Path == "/api/v1/messages/deliver" {
			s.mu.Lock()
			s.status = append(s.status, rec.Code)
			s.bodies = append(s.bodies, rec.Body.String())
			s.mu.Unlock()
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(srv.Close)
	s.api = apiclient.New(srv.URL)
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certSrv.Close)
	s.rootCAs = x509.NewCertPool()
	s.rootCAs.AddCert(certSrv.Certificate())
	freePort := func() int {
		free, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer free.Close()
		return free.Addr().(*net.TCPAddr).Port
	}
	port, subPort := freePort(), freePort()
	gateway := smtpgw.NewServer("mail.example.test", s.api,
		&tls.Config{Certificates: certSrv.TLS.Certificates, MinVersion: tls.VersionTLS12},
		smtpgw.NewStore(f.db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 100}))
	if err := gateway.ListenAndServe(smtpgw.SMTPPorts{Inbound: port, SubmissionTLS: subPort}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gateway.Shutdown)
	s.addr = fmt.Sprintf("127.0.0.1:%d", port)
	s.subAddr = fmt.Sprintf("127.0.0.1:%d", subPort)
	return s
}

// deliver sends a message in one real SMTP session and answers the reply to its end of data.
func (s *eightBitServer) deliver(from, to, message string) (string, []int, string) {
	s.t.Helper()
	s.mu.Lock()
	s.status, s.bodies = nil, nil
	s.mu.Unlock()
	c, err := smtp.Dial(s.addr)
	if err != nil {
		s.t.Fatal(err)
	}
	defer c.Close()
	if err := c.Hello("mx.remote.test"); err != nil {
		s.t.Fatal(err)
	}
	if ok, _ := c.Extension("8BITMIME"); !ok {
		s.t.Fatal("8BITMIME is not offered")
	}
	reply := "250"
	if err := send(c, from, to, message); err != nil {
		reply = err.Error()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	body := ""
	if len(s.bodies) != 0 {
		body = s.bodies[len(s.bodies)-1]
	}
	return reply, append([]int(nil), s.status...), body
}

// pastGreylist records that this sender has already retried past greylisting.
func (s *eightBitServer) pastGreylist(sender string) {
	s.t.Helper()
	now := time.Now()
	for _, ip := range []string{"127.0.0.1", "192.0.2.10"} {
		if err := s.db.Create(&models.GreylistEntry{Sender: sender, Recipient: s.address, SourceIP: ip, FirstSeen: now.Add(-time.Hour), RetryAfter: now.Add(-time.Minute), Passed: true, ExpiresAt: now.Add(time.Hour)}).Error; err != nil {
			s.t.Fatal(err)
		}
	}
}

// newest is the last message the mailbox received.
func (s *eightBitServer) newest() models.Message {
	s.t.Helper()
	var stored models.Message
	if err := s.db.Where("mailbox_id = ?", s.mailboxID).Order("id DESC").First(&stored).Error; err != nil {
		s.t.Fatalf("no message stored: %v", err)
	}
	return stored
}

// proveStored checks the newest message holds message's exact octets after the trace fields
// this server prepends, sizes them truly, and reads back exactly through the API, IMAP and POP3.
func (s *eightBitServer) proveStored(message, password string) {
	s.t.Helper()
	stored := s.newest()
	source := stored.Source()
	if !bytes.HasSuffix(source, []byte(message)) {
		s.t.Fatalf("stored octets changed:\n got %q\nwant suffix %q", source, message)
	}
	if !models.SourceFitsText(message) && (stored.RawOctets == nil || stored.RawMessage != "") {
		s.t.Fatalf("source not in raw_octets: text %d octets, raw_octets %d", len(stored.RawMessage), len(stored.RawOctets))
	}
	if stored.RawSize != len(source) || stored.DowngradedSize != len(source) {
		s.t.Fatalf("sizes: raw_size %d, downgraded_size %d, source %d", stored.RawSize, stored.DowngradedSize, len(source))
	}
	for name, text := range map[string]string{"subject": stored.Subject, "body_text": stored.BodyText, "body_html": stored.BodyHTML} {
		if !models.SourceFitsText(text) {
			s.t.Fatalf("%s is not readable text: %q", name, text)
		}
	}
	raw := s.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/raw", stored.ID), s.session, "")
	if raw.Code != http.StatusOK || !bytes.Equal(raw.Body.Bytes(), source) {
		s.t.Fatalf("API raw: %d, %d octets, want %d", raw.Code, raw.Body.Len(), len(source))
	}
	// A literal may not hold a NUL (RFC 3501 section 9). IMAP refuses to FETCH that message, as it
	// does an imported one, so POP3 and the API are how it is read; #1921 tracks that.
	if got, size, refused := s.imapFetch(password); refused != (bytes.IndexByte(source, 0) >= 0) {
		s.t.Fatalf("IMAP FETCH refused = %v for a source with %d NUL octets", refused, bytes.Count(source, []byte{0}))
	} else if !refused && (!bytes.Equal(got, source) || size != len(source)) {
		s.t.Fatalf("IMAP FETCH BODY.PEEK[] changed the message (RFC822.SIZE %d, want %d):\n got %q\nwant %q", size, len(source), got, source)
	}
	if got, size := s.pop3Retr(password); !bytes.Equal(got, source) || size != len(source) {
		s.t.Fatalf("POP3 RETR changed the message (LIST %d, want %d):\n got %q\nwant %q", size, len(source), got, source)
	}
}

// imapFetch signs in over a real IMAP session and returns the newest message, its RFC822.SIZE,
// and whether FETCH refused it.
func (s *eightBitServer) imapFetch(password string) ([]byte, int, bool) {
	s.t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	session := imapserver.NewSession(server, imapgw.NewBackend(s.api, nil), "imap.test", nil, goimap.NopLimiter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	defer func() {
		_ = client.Close()
		<-done
	}()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(client)
	if _, err := reader.ReadString('\n'); err != nil {
		s.t.Fatal(err)
	}
	var literal []byte
	size := -1
	exists := 0
	command := func(tag, line string) bool {
		s.t.Helper()
		if _, err := fmt.Fprintf(client, "%s %s\r\n", tag, line); err != nil {
			s.t.Fatal(err)
		}
		for {
			text, err := reader.ReadString('\n')
			if err != nil {
				s.t.Fatalf("IMAP %s: %v", line, err)
			}
			if m := regexp.MustCompile(`^\* (\d+) EXISTS`).FindStringSubmatch(text); m != nil {
				exists, _ = strconv.Atoi(m[1])
			}
			if m := regexp.MustCompile(`RFC822\.SIZE (\d+)`).FindStringSubmatch(text); m != nil {
				size, _ = strconv.Atoi(m[1])
			}
			if m := regexp.MustCompile(`\{(\d+)\}\r\n$`).FindStringSubmatch(text); m != nil {
				n, _ := strconv.Atoi(m[1])
				literal = make([]byte, n)
				if _, err := io.ReadFull(reader, literal); err != nil {
					s.t.Fatal(err)
				}
				continue
			}
			if strings.HasPrefix(text, tag+" ") {
				return strings.HasPrefix(text, tag+" OK")
			}
		}
	}
	if !command("a1", fmt.Sprintf("LOGIN %q %q", s.address, password)) || !command("a2", "SELECT INBOX") {
		s.t.Fatal("IMAP sign-in or SELECT refused")
	}
	refused := !command("a3", fmt.Sprintf("FETCH %d (RFC822.SIZE BODY.PEEK[])", exists))
	return literal, size, refused
}

// pop3Retr signs in over a real POP3 session and returns the newest message, undoing its
// byte-stuffing, and the size LIST gives it.
func (s *eightBitServer) pop3Retr(password string) ([]byte, int) {
	s.t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	session := pop3server.NewSession(server, pop3gw.NewBackend(s.api, nil), nil, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	defer func() {
		_ = client.Close()
		<-done
	}()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(client)
	if _, err := reader.ReadString('\n'); err != nil {
		s.t.Fatal(err)
	}
	command := func(line string) string {
		s.t.Helper()
		if _, err := fmt.Fprintf(client, "%s\r\n", line); err != nil {
			s.t.Fatal(err)
		}
		text, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(text, "+OK") {
			s.t.Fatalf("POP3 %s: %q %v", line, text, err)
		}
		return text
	}
	command("USER " + s.address)
	command("PASS " + password)
	fields := strings.Fields(command("STAT"))
	count, _ := strconv.Atoi(fields[1])
	listing := strings.Fields(command(fmt.Sprintf("LIST %d", count)))
	size, _ := strconv.Atoi(listing[2])
	command(fmt.Sprintf("RETR %d", count))
	var out []byte
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			s.t.Fatalf("POP3 RETR: %v", err)
		}
		if string(line) == ".\r\n" {
			return out, size
		}
		out = append(out, bytes.TrimPrefix(line, []byte("."))...)
	}
}

// TestEightBitMailDeliversThroughSMTP sends each message a real sender may, in a real SMTP
// session, and proves it arrives with its exact octets and reads back through IMAP and POP3 (#1921).
func TestEightBitMailDeliversThroughSMTP(t *testing.T) {
	useRealGrants(t)
	s := newEightBitServer(t)
	s.pastGreylist("sender@remote.test")
	password := s.createAppPassword("reader").Password
	for _, shape := range eightBitShapes() {
		t.Run(shape.name, func(t *testing.T) {
			s.t = t
			reply, status, body := s.deliver("sender@remote.test", s.address, shape.message)
			if reply != "250" {
				t.Fatalf("SMTP %s, API %v %s", reply, status, body)
			}
			if len(status) != 1 || status[0] != http.StatusCreated {
				t.Fatalf("API %v %s", status, body)
			}
			s.proveStored(shape.message, password)
		})
	}
}

// TestEightBitSubmissionReachesEveryRecipient sends the same mail from a mail client: to a local
// recipient and to the outbound queue, where the row must hold it exactly and give it back.
func TestEightBitSubmissionReachesEveryRecipient(t *testing.T) {
	useRealGrants(t)
	s := newEightBitServer(t)
	password := s.createAppPassword("client").Password
	// Four shapes: each sends twice, and the submission rate limit is ten.
	for _, shape := range eightBitShapes() {
		switch shape.name {
		case "Latin-1 declared", "8-bit no charset", "binary attachment", "NUL in text body":
		default:
			continue
		}
		t.Run(shape.name, func(t *testing.T) {
			s.t = t
			message := strings.Replace(strings.Replace(shape.message, "Sender <sender@remote.test>", s.address, 1), "From: sender@remote.test", "From: "+s.address, 1)
			if err := send(dialSubmission(t, s.subAddr, s.rootCAs, s.address, password), s.address, s.address, message); err != nil {
				t.Fatalf("to a local recipient: %v", err)
			}
			if !bytes.HasSuffix(s.newest().Source(), []byte(message)) {
				t.Fatalf("local copy changed: %q", s.newest().Source())
			}
			if err := send(dialSubmission(t, s.subAddr, s.rootCAs, s.address, password), s.address, "someone@elsewhere.test", message); err != nil {
				t.Fatalf("to a remote recipient: %v", err)
			}
			var queued models.OutboundQueue
			if err := s.db.Where("recipient = ?", "someone@elsewhere.test").Order("id DESC").First(&queued).Error; err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(queued.RawMessage, message) {
				t.Fatalf("queued message changed: %q", queued.RawMessage)
			}
			// The worker claims a row with a raw query, not through Find.
			var claimed models.OutboundQueue
			if err := s.db.Raw("SELECT * FROM outbound_queue WHERE id = ?", queued.ID).Scan(&claimed).Error; err != nil {
				t.Fatal(err)
			}
			claimed.JoinSource()
			if claimed.RawMessage != queued.RawMessage {
				t.Fatalf("claimed row changed the message: %q", claimed.RawMessage)
			}
		})
	}
}

// TestEightBitMailStoresThroughRestmailAndImport proves the other two ways in keep the octets.
func TestEightBitMailStoresThroughRestmailAndImport(t *testing.T) {
	useRealGrants(t)
	s := newEightBitServer(t)
	s.own(s.mailboxID, s.address)
	s.pastGreylist("sender@remote.test")
	password := s.createAppPassword("reader").Password
	for _, shape := range eightBitShapes() {
		t.Run("RESTMAIL "+shape.name, func(t *testing.T) {
			s.t = t
			payload, _ := json.Marshal(map[string]any{"from": "sender@remote.test", "to": []string{s.address}, "raw_message": []byte(shape.message)})
			if rr := s.do(http.MethodPost, "/restmail/messages", "", string(payload)); rr.Code != http.StatusCreated {
				t.Fatalf("%d %s", rr.Code, rr.Body)
			}
			s.proveStored(shape.message, password)
		})
		t.Run("import "+shape.name, func(t *testing.T) {
			s.t = t
			if strings.Contains(shape.message, "a.bin") {
				t.Skip("the import route's filesystem root is /attachments; message_import_stored_test covers an attachment")
			}
			message := strings.Replace(shape.message, "Message-ID: <", "Message-ID: <import-", 1)
			payload, _ := json.Marshal(map[string]any{"raw_message": []byte(message), "folder": "INBOX", "internal_date": "2026-11-04T12:00:00Z"})
			if rr := s.do(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", s.accountID), s.session, string(payload)); rr.Code != http.StatusOK {
				t.Fatalf("%d %s", rr.Code, rr.Body)
			}
			s.proveStored(message, password)
		})
	}
}
