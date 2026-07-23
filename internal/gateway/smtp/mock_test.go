package smtp

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
)

// mockBackend is an in-memory Backend: it decides recipient locality and
// delivery success without a live API.
type mockBackend struct {
	mu sync.Mutex

	user      string
	pass      string
	token     string
	accountID uint

	local       map[string]bool // addresses CheckMailbox reports as existing
	checkErr    map[string]bool // addresses where CheckMailbox errors (temp fail)
	deliverFail map[string]bool // local addresses whose DeliverMessage errors

	delivered []string // recorded successful local deliveries (by Address)
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		user:        "alice@example.com",
		pass:        "s3cret",
		token:       "tok-alice",
		accountID:   42,
		local:       map[string]bool{},
		checkErr:    map[string]bool{},
		deliverFail: map[string]bool{},
	}
}

func (m *mockBackend) Login(email, password string) (*apiclient.LoginResponse, error) {
	if email != m.user || password != m.pass {
		return nil, &apiclient.APIError{StatusCode: 401, Body: "invalid credentials"}
	}
	resp := &apiclient.LoginResponse{}
	resp.Data.AccessToken = m.token
	resp.Data.User.ID = m.accountID
	resp.Data.User.Email = email
	return resp, nil
}

func (m *mockBackend) CheckMailbox(address string) (*apiclient.MailboxCheckResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checkErr[address] {
		return nil, &apiclient.APIError{StatusCode: 503, Body: "service unavailable"}
	}
	resp := &apiclient.MailboxCheckResponse{}
	resp.Data.Exists = m.local[address]
	resp.Data.Address = address
	return resp, nil
}

func (m *mockBackend) DeliverMessage(req *apiclient.DeliverRequest) (*apiclient.DeliverResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deliverFail[req.Address] {
		return nil, &apiclient.APIError{StatusCode: 451, Body: "temporary local failure"}
	}
	m.delivered = append(m.delivered, req.Address)
	resp := &apiclient.DeliverResponse{}
	resp.Data.ID = 1000 + uint(len(m.delivered))
	return resp, nil
}

func (m *mockBackend) deliveredTo() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.delivered...)
}

// mockStore is an in-memory Store: it decides sender authorization and records
// outbound queue inserts, without a database.
type mockStore struct {
	mu sync.Mutex

	authorized map[string]bool // MAIL FROM address -> allowed for the account
	authErr    error           // forced SenderAuthorized error
	enqueueErr error           // forced EnqueueOutbound error

	enqueued []OutboundMessage
}

func newMockStore() *mockStore {
	return &mockStore{authorized: map[string]bool{}}
}

func (s *mockStore) SenderAuthorized(_ uint, from string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authErr != nil {
		return false, s.authErr
	}
	return s.authorized[from], nil
}

func (s *mockStore) EnqueueOutbound(msg OutboundMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enqueueErr != nil {
		return s.enqueueErr
	}
	s.enqueued = append(s.enqueued, msg)
	return nil
}

func (s *mockStore) queued() []OutboundMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]OutboundMessage(nil), s.enqueued...)
}

// ── Transcript harness ────────────────────────────────────────────────

type smtpHarness struct {
	t     *testing.T
	back  *mockBackend
	store *mockStore
	conn  net.Conn
	cr    *bufio.Reader
	cw    *bufio.Writer
	done  chan struct{}
}

// newSMTPHarness builds a transcript harness. Optional configure funcs run on
// the *Server before the go-smtp server is built, mirroring how production
// applies settings (e.g. SetMaxMessageSize) between NewServer and listen.
func newSMTPHarness(t *testing.T, back *mockBackend, store *mockStore, isSubmission bool, configure ...func(*Server)) *smtpHarness {
	t.Helper()
	client, server := net.Pipe()
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 1000})
	// Build the go-smtp server exactly as production does (same construction
	// path), then serve the pipe's server end as a single connection.
	s := NewServer("smtp.test", back, nil, store, limiter)
	for _, fn := range configure {
		fn(s)
	}
	srv := s.newSMTPServer(isSubmission)

	// Wrap the served conn exactly as the production accept path does, so the
	// anti-slowloris machinery is in the loop under test too.
	listener := newOneConnListener(newTransferRateConn(server, s.transferPolicy))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(listener)
	}()

	h := &smtpHarness{
		t:     t,
		back:  back,
		store: store,
		conn:  client,
		cr:    bufio.NewReader(client),
		cw:    bufio.NewWriter(client),
		done:  done,
	}
	t.Cleanup(func() {
		_ = client.Close()
		<-h.done
	})
	// Registered second so it runs first (LIFO): closing the server unblocks
	// Serve's Accept, letting h.done complete in the cleanup above.
	t.Cleanup(func() {
		_ = srv.Close()
	})

	// Greeting.
	if _, final := h.readReply(); !strings.HasPrefix(final, "220") {
		t.Fatalf("greeting = %q, want 220...", final)
	}
	return h
}

func (h *smtpHarness) readLine() string {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := h.cr.ReadString('\n')
	if err != nil {
		h.t.Fatalf("readLine: %v (partial %q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

// readReply reads a (possibly multi-line) SMTP reply and returns all lines plus
// the final line. Continuation lines have a '-' as the 4th character; the final
// line has a ' '.
func (h *smtpHarness) readReply() (lines []string, final string) {
	h.t.Helper()
	for {
		l := h.readLine()
		lines = append(lines, l)
		if len(l) < 4 || l[3] == ' ' {
			return lines, l
		}
	}
}

func (h *smtpHarness) send(format string, args ...interface{}) {
	h.t.Helper()
	_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(h.cw, format+"\r\n", args...)
	if err := h.cw.Flush(); err != nil {
		h.t.Fatalf("send: %v", err)
	}
}

// cmd sends a command and returns the final reply line (e.g. "250 OK").
func (h *smtpHarness) cmd(format string, args ...interface{}) string {
	h.t.Helper()
	h.send(format, args...)
	_, final := h.readReply()
	return final
}

func replyCode(line string) string {
	if len(line) < 3 {
		return ""
	}
	return line[:3]
}

func (h *smtpHarness) ehlo() {
	h.t.Helper()
	if _, final := h.readReplyAfter("EHLO client.test"); !strings.HasPrefix(final, "250") {
		h.t.Fatalf("EHLO final = %q", final)
	}
}

func (h *smtpHarness) readReplyAfter(format string, args ...interface{}) (lines []string, final string) {
	h.send(format, args...)
	return h.readReply()
}

// authPlain performs AUTH PLAIN with inline credentials.
func (h *smtpHarness) authPlain(user, pass string) string {
	h.t.Helper()
	raw := "\x00" + user + "\x00" + pass
	enc := base64.StdEncoding.EncodeToString([]byte(raw))
	return h.cmd("AUTH PLAIN %s", enc)
}

// dataBody sends DATA, the message body, and the terminating dot, returning the
// final reply after the dot.
func (h *smtpHarness) dataBody(body string) string {
	h.t.Helper()
	if r := h.cmd("DATA"); !strings.HasPrefix(r, "354") {
		h.t.Fatalf("DATA = %q, want 354...", r)
	}
	for _, line := range strings.Split(body, "\n") {
		h.send("%s", strings.TrimRight(line, "\r"))
	}
	h.send(".")
	_, final := h.readReply()
	return final
}

// oneConnListener hands a single pre-established connection (the server end of
// a net.Pipe) to a listener-based server, then blocks until closed.
type oneConnListener struct {
	mu     sync.Mutex
	conn   net.Conn
	closed chan struct{}
}

func newOneConnListener(conn net.Conn) *oneConnListener {
	return &oneConnListener{conn: conn, closed: make(chan struct{})}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	conn := l.conn
	l.conn = nil
	l.mu.Unlock()
	if conn != nil {
		return conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return oneConnAddr{} }

type oneConnAddr struct{}

func (oneConnAddr) Network() string { return "pipe" }
func (oneConnAddr) String() string  { return "pipe" }
