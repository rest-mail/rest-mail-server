package api

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"
	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/digest"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
	pop3gw "github.com/rest-mail/rest-mail-server/internal/gateway/pop3"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/sendlimits"
)

// generatedMailSessions is a real API with one mailbox's app password, for real gateway sessions.
type generatedMailSessions struct {
	f        *credentialFixture
	api      *apiclient.Client
	password string
}

func newGeneratedMailSessions(t *testing.T) *generatedMailSessions {
	t.Helper()
	f := newCredentialFixture(t)
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	return &generatedMailSessions{f: f, api: apiclient.New(srv.URL), password: f.createAppPassword("mail client").Password}
}

// pop3Sizes runs a POP3 session and returns what STAT, LIST n, LIST and RETR's status report,
// and the octets RETR sent, with dot-stuffing and the terminator removed.
func (s *generatedMailSessions) pop3Sizes(t *testing.T, utf8 bool) (stat, listed, listedAll, status int, sent []byte) {
	t.Helper()
	client, server := net.Pipe()
	session := pop3server.NewSession(server, pop3gw.NewBackend(s.api, nil), nil, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	if err := client.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(client)
	line := func() string {
		t.Helper()
		l, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("POP3 read: %v", err)
		}
		return l
	}
	do := func(command string) string {
		t.Helper()
		if _, err := fmt.Fprintf(client, "%s\r\n", command); err != nil {
			t.Fatalf("POP3 write: %v", err)
		}
		l := line()
		if !strings.HasPrefix(l, "+OK") {
			t.Fatalf("POP3 %s: %q", strings.Fields(command)[0], l)
		}
		return l
	}
	// lines reads a multi-line answer to its terminator, removing dot-stuffing.
	lines := func() []byte {
		t.Helper()
		var out []byte
		for {
			l := line()
			if l == ".\r\n" {
				return out
			}
			out = append(out, strings.TrimPrefix(l, ".")...)
		}
	}
	line() // greeting
	if utf8 {
		do("UTF8")
	}
	do("USER " + s.f.address)
	do("PASS " + s.password)
	if _, err := fmt.Sscanf(do("STAT"), "+OK 1 %d", &stat); err != nil {
		t.Fatalf("STAT: %v", err)
	}
	if _, err := fmt.Sscanf(do("LIST 1"), "+OK 1 %d", &listed); err != nil {
		t.Fatalf("LIST 1: %v", err)
	}
	do("LIST")
	if _, err := fmt.Sscanf(string(lines()), "1 %d\r\n", &listedAll); err != nil {
		t.Fatalf("LIST: %v", err)
	}
	if _, err := fmt.Sscanf(do("RETR 1"), "+OK %d octets", &status); err != nil {
		t.Fatalf("RETR: %v", err)
	}
	return stat, listed, listedAll, status, lines()
}

// imapSizes runs an IMAP session and returns what RFC822.SIZE reports and the octets BODY[] sent.
func (s *generatedMailSessions) imapSizes(t *testing.T, utf8 bool) (size int, sent []byte) {
	t.Helper()
	client, server := net.Pipe()
	session := imapserver.NewSession(server, imapgw.NewBackend(s.api, nil), "imap.test", nil, goimap.NopLimiter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	if err := client.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(client)
	line := func() string {
		t.Helper()
		l, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("IMAP read: %v", err)
		}
		return l
	}
	// do sends a command and reads to its tagged OK, returning the untagged data and any literal.
	do := func(tag, command string) (data string, literal []byte) {
		t.Helper()
		if _, err := fmt.Fprintf(client, "%s %s\r\n", tag, command); err != nil {
			t.Fatalf("IMAP write: %v", err)
		}
		for {
			l := line()
			if strings.HasPrefix(l, tag+" ") {
				if !strings.HasPrefix(l, tag+" OK") {
					t.Fatalf("IMAP %s: %q", strings.Fields(command)[0], l)
				}
				return data, literal
			}
			data += l
			if open := strings.LastIndexByte(l, '{'); open >= 0 && strings.HasSuffix(l, "}\r\n") {
				n, err := strconv.Atoi(l[open+1 : len(l)-3])
				if err != nil {
					t.Fatalf("IMAP literal in %q: %v", l, err)
				}
				literal = make([]byte, n)
				if _, err := readFull(r, literal); err != nil {
					t.Fatalf("IMAP literal: %v", err)
				}
			}
		}
	}
	line() // greeting
	do("a1", fmt.Sprintf("LOGIN %q %q", s.f.address, s.password))
	if utf8 {
		do("a2", "ENABLE UTF8=ACCEPT")
	}
	do("a3", "SELECT INBOX")
	data, literal := do("a4", "FETCH 1 (RFC822.SIZE BODY.PEEK[])")
	at := strings.Index(data, "RFC822.SIZE ")
	if at < 0 {
		t.Fatalf("no RFC822.SIZE in %q", data)
	}
	if _, err := fmt.Sscanf(data[at:], "RFC822.SIZE %d", &size); err != nil {
		t.Fatalf("RFC822.SIZE in %q: %v", data, err)
	}
	return size, literal
}

func readFull(r *bufio.Reader, into []byte) (int, error) {
	total := 0
	for total < len(into) {
		n, err := r.Read(into[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// proveSizes reads the mailbox's one INBOX message through every session a client can open: each
// size reported must count the octets sent, and those octets must be the stored row's raw source.
func (s *generatedMailSessions) proveSizes(t *testing.T) (models.Message, []byte) {
	t.Helper()
	var row models.Message
	if err := s.f.db.Where("mailbox_id = ? AND folder = ?", s.f.mailboxID, "INBOX").Take(&row).Error; err != nil {
		t.Fatalf("the mailbox's INBOX: %v", err)
	}
	if row.RawMessage == "" || row.RawSize != len(row.RawMessage) || row.SizeBytes != row.RawSize {
		t.Fatalf("stored raw %d octets, raw_size %d, size_bytes %d", len(row.RawMessage), row.RawSize, row.SizeBytes)
	}
	var sent []byte
	for _, utf8 := range []bool{false, true} {
		t.Run(fmt.Sprintf("utf8=%v", utf8), func(t *testing.T) {
			stat, listed, listedAll, status, retr := s.pop3Sizes(t, utf8)
			for name, got := range map[string]int{"STAT": stat, "LIST 1": listed, "LIST": listedAll, "RETR status": status} {
				if got != len(retr) {
					t.Errorf("POP3 %s reports %d, RETR sent %d octets", name, got, len(retr))
				}
			}
			size, body := s.imapSizes(t, utf8)
			if size != len(body) {
				t.Errorf("RFC822.SIZE reports %d, BODY[] sent %d octets", size, len(body))
			}
			if string(retr) != row.RawMessage {
				t.Errorf("RETR sent\n%q\nthe row stores\n%q", retr, row.RawMessage)
			}
			if string(body) != row.RawMessage {
				t.Errorf("BODY[] sent\n%q\nthe row stores\n%q", body, row.RawMessage)
			}
			sent = retr
		})
	}
	if sent == nil {
		t.Fatal("no session read the message")
	}
	return row, sent
}

// proveGeneratedMessage holds a generated message to RFC 5322: the headers it requires, every
// line ending in CRLF and within 998 octets, and 7-bit throughout.
func proveGeneratedMessage(t *testing.T, sent []byte, from, to, subject string) {
	t.Helper()
	message, err := mail.ReadMessage(bytes.NewReader(sent))
	if err != nil {
		t.Fatalf("the message does not parse: %v\n%q", err, sent)
	}
	for name, want := range map[string]string{"From": "<" + from + ">", "To": "<" + to + ">", "Subject": subject, "Auto-Submitted": "auto-generated"} {
		if got := message.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, err := message.Header.Date(); err != nil {
		t.Errorf("Date %q: %v", message.Header.Get("Date"), err)
	}
	if id := message.Header.Get("Message-ID"); !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") || !strings.Contains(id, "@") {
		t.Errorf("Message-ID = %q", id)
	}
	if !bytes.HasSuffix(sent, []byte("\r\n")) {
		t.Error("the last line has no line break")
	}
	for i, l := range bytes.Split(sent, []byte("\r\n")) {
		if bytes.ContainsAny(l, "\r\n") {
			t.Errorf("line %d holds a bare line break: %q", i, l)
		}
		if len(l) > 998 {
			t.Errorf("line %d has %d octets", i, len(l))
		}
	}
	for _, octet := range sent {
		if octet >= 0x80 {
			t.Fatalf("8-bit octet %#x in the message", octet)
		}
	}
}

// A digest quoting what strangers sent, with 8-bit text, a subject longer than any line may be
// and a preview that is cut inside a character, reads back the same size through POP3 and IMAP.
func TestQuarantineDigestSizesThroughRealSessions(t *testing.T) {
	s := newGeneratedMailSessions(t)
	longSubject := strings.Repeat("offer ", 300) + "Grüße"
	preview := strings.Repeat("a", 99) + "ü and the rest"
	for _, item := range []models.Quarantine{
		{MailboxID: s.f.mailboxID, Sender: "jörg@spam.example", Subject: longSubject, BodyPreview: preview, QuarantineReason: "spam", ReceivedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
		{MailboxID: s.f.mailboxID, Sender: "x@spam.example", Subject: ".\r\n.leading", BodyPreview: ".", QuarantineReason: "virus", ReceivedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
	} {
		if err := s.f.db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	digest.NewWorker(s.f.db, time.Hour).SendDigests()

	row, sent := s.proveSizes(t)
	const subject = "Quarantine Digest: 2 message(s) held"
	proveGeneratedMessage(t, sent, "quarantine-digest@example.test", s.f.address, subject)
	if row.MsgID == "" || row.ThreadID != row.MsgID || row.ReceivedAt.IsZero() {
		t.Errorf("message id %q, thread %q, received %v", row.MsgID, row.ThreadID, row.ReceivedAt)
	}
	if row.IMAPSize != len(sent) || row.DowngradedSize != len(sent) || row.POP3UTF8Size != len(sent) {
		t.Errorf("measured imap %d, downgraded %d, pop3 %d, sent %d", row.IMAPSize, row.DowngradedSize, row.POP3UTF8Size, len(sent))
	}
	parsed, err := rmime.Parse(sent)
	if err != nil {
		t.Fatalf("parse what RETR sent: %v", err)
	}
	if len(parsed.Body.Parts) != 2 {
		t.Fatalf("%d body parts, want text and HTML", len(parsed.Body.Parts))
	}
	normal := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
	if got := normal(parsed.Body.Parts[0].Content); got != row.BodyText {
		t.Errorf("the text part reads\n%q\nthe row holds\n%q", got, row.BodyText)
	}
	if got := parsed.Body.Parts[1].Content; got != row.BodyHTML {
		t.Errorf("the HTML part reads\n%q\nthe row holds\n%q", got, row.BodyHTML)
	}
	for _, want := range []string{longSubject, "jörg@spam.example", strings.Repeat("a", 99) + "..."} {
		if !strings.Contains(row.BodyText, want) {
			t.Errorf("the digest text lacks %q", want)
		}
	}
}

// The test route's message is stored and read back as the same source.
func TestTestRouteMessageSizesThroughRealSessions(t *testing.T) {
	// The route asks for the wildcard; who holds it is not what this test is about.
	previous := heldGrants
	heldGrants = func(*grants.Cache) middleware.HeldGrants {
		return func(uint) (grants.Held, error) {
			return grants.HeldOf(grants.Entry{Capability: grants.Wildcard, Scope: grants.All}), nil
		}
	}
	t.Cleanup(func() { heldGrants = previous })
	s := newGeneratedMailSessions(t)
	const body = `line one\nGrüße\n.dot\n`
	rr := s.f.do(http.MethodPost, testEmailSendPath, s.f.session,
		fmt.Sprintf(`{"from":%q,"to":%q,"subject":"Fixture","body":"%s"}`, s.f.address, s.f.address, body))
	if rr.Code != http.StatusCreated {
		t.Fatalf("test send: %d %s", rr.Code, rr.Body.String())
	}
	_, sent := s.proveSizes(t)
	proveGeneratedMessage(t, sent, s.f.address, s.f.address, "Fixture")

	var rows []models.Message
	if err := s.f.db.Where("mailbox_id = ?", s.f.mailboxID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].RawMessage != rows[1].RawMessage {
		t.Fatalf("%d copies, or their sources differ", len(rows))
	}
}

// So does the notice that sending has reached its limit.
func TestSendLimitNoticeSizesThroughRealSessions(t *testing.T) {
	s := newGeneratedMailSessions(t)
	svc := sendlimits.Service{Defaults: models.SendLimitValues{MessagesPerHour: 1}}
	id := s.f.mailboxID
	attempt := sendlimits.Attempt{AccountID: id, SenderID: id, Recipients: 1, Entry: &models.AuditEntry{
		ActorKind: models.AuditActorAccount, ActorID: &id, ActorName: s.f.address, Method: http.MethodPost, Route: "/api/v1/messages/send", Status: http.StatusAccepted,
	}}
	if hit, err := svc.Reserve(s.f.db, attempt); err != nil || hit != nil {
		t.Fatalf("the first message: %+v, %v", hit, err)
	}
	if hit, err := svc.Reserve(s.f.db, attempt); err != nil || hit == nil {
		t.Fatalf("the message past the limit: %+v, %v", hit, err)
	}
	_, sent := s.proveSizes(t)
	proveGeneratedMessage(t, sent, "postmaster@localhost", s.f.address, "REST Mail sending limit reached")
}
