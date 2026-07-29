package smtp

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
	"github.com/restmail/restmail/internal/mtls/mtlstest"
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
	deliverFail map[string]bool // local addresses whose DeliverMessage errors (451)

	// deliverStatus[address] = N: DeliverMessage for that local recipient returns
	// an APIError with HTTP status N (e.g. 422 mailbox_full), letting a test drive
	// the status-code → SMTP-reply mapping.
	deliverStatus map[string]int

	// checkErrAfter[address] = N: the first N CheckMailbox calls for that
	// address behave normally; the (N+1)th and every later call return a
	// transient error. Models an API outage that strikes between the RCPT-time
	// check (call 1) and the DATA-time re-check (call 2) for the same recipient.
	checkErrAfter map[string]int
	checkCount    map[string]int // per-address CheckMailbox invocation count

	// loginErr, when non-nil, is returned by Login instead of verifying
	// credentials — used to simulate a transient API/network failure.
	loginErr   error
	loginCalls int // number of times Login was invoked (brute-force hard-stop assertions)

	delivered   []string                    // recorded successful local deliveries (by Address)
	deliverReqs []*apiclient.DeliverRequest // full deliver requests, for asserting captured fields
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		user:          "alice@example.com",
		pass:          "s3cret",
		token:         "tok-alice",
		accountID:     42,
		local:         map[string]bool{},
		checkErr:      map[string]bool{},
		deliverFail:   map[string]bool{},
		deliverStatus: map[string]int{},
		checkErrAfter: map[string]int{},
		checkCount:    map[string]int{},
	}
}

func (m *mockBackend) Login(email, password string) (*apiclient.LoginResponse, error) {
	m.mu.Lock()
	m.loginCalls++
	loginErr := m.loginErr
	m.mu.Unlock()
	if loginErr != nil {
		return nil, loginErr
	}
	if email != m.user || password != m.pass {
		return nil, &apiclient.APIError{StatusCode: 401, Body: "invalid credentials"}
	}
	resp := &apiclient.LoginResponse{}
	resp.Data.AccessToken = m.token
	resp.Data.User.ID = m.accountID
	resp.Data.User.Email = email
	return resp, nil
}

// loginCallCount returns how many times Login has been invoked.
func (m *mockBackend) loginCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loginCalls
}

func (m *mockBackend) CheckMailbox(address string) (*apiclient.MailboxCheckResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkCount[address]++
	if m.checkErr[address] {
		return nil, &apiclient.APIError{StatusCode: 503, Body: "service unavailable"}
	}
	if n, ok := m.checkErrAfter[address]; ok && m.checkCount[address] > n {
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
	if status, ok := m.deliverStatus[req.Address]; ok {
		return nil, &apiclient.APIError{StatusCode: status, Body: `{"error":"mailbox_full"}`}
	}
	m.deliverReqs = append(m.deliverReqs, req)
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

// lastDeliverReq returns the most recent DeliverRequest the backend received, or
// nil if none. Used to assert the SMTP session captured inbound transport
// security onto the deliver call.
func (m *mockBackend) lastDeliverReq() *apiclient.DeliverRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.deliverReqs) == 0 {
		return nil
	}
	return m.deliverReqs[len(m.deliverReqs)-1]
}

// mockStore is an in-memory Store: it decides sender authorization and records
// outbound queue inserts, without a database.
type mockStore struct {
	mu sync.Mutex

	authorized map[string]bool // MAIL FROM address -> allowed for the account
	authErr    error           // forced SenderAuthorized error
	enqueueErr error           // forced EnqueueOutbound error
	persistErr error           // forced PersistSubmittedMessage error

	enqueued  []OutboundMessage
	persisted []SubmittedMessage
	nextMsgID uint // assigned to the most recent persisted submission
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

func (s *mockStore) PersistSubmittedMessage(msg SubmittedMessage) (*uint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.persistErr != nil {
		return nil, s.persistErr
	}
	s.persisted = append(s.persisted, msg)
	s.nextMsgID++
	id := s.nextMsgID
	return &id, nil
}

func (s *mockStore) persistedSubmissions() []SubmittedMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SubmittedMessage(nil), s.persisted...)
}

// lastPersistedID returns the id assigned to the most recent persisted
// submission (0 if none).
func (s *mockStore) lastPersistedID() uint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextMsgID
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
	t       *testing.T
	back    *mockBackend
	store   *mockStore
	limiter *connlimiter.Limiter
	conn    net.Conn
	cr      *bufio.Reader
	cw      *bufio.Writer
	done    chan struct{}
}

// harnessTLSConfig issues a throwaway server keypair so the harness can present TLS the
// way production does. Without one, go-smtp does not advertise STARTTLS and sets
// AllowInsecureAuth, so a plaintext harness exercised a configuration that cannot exist
// in a deployment.
func harnessTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	ca, err := mtlstest.NewCA("harness-ca")
	if err != nil {
		t.Fatalf("test CA: %v", err)
	}
	certPEM, keyPEM, err := ca.IssueServer("smtp.test", []string{"smtp.test"}, nil,
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("issue server cert: %v", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
}

// newSMTPHarness builds a harness whose session is encrypted, as every deployed listener
// is: implicit TLS on 465/993/995, and STARTTLS before anything is accepted on 25. Mail
// is not accepted in the clear, so a cleartext harness could not complete a transaction
// at all.
//
// Use newCleartextSMTPHarness for the tests that are specifically about what a cleartext
// session is refused.
//
// Optional configure funcs run on the *Server before the go-smtp server is built,
// mirroring how production applies settings (e.g. SetMaxMessageSize) between NewServer
// and listen.
func newSMTPHarness(t *testing.T, back *mockBackend, store *mockStore, isSubmission bool, configure ...func(*Server)) *smtpHarness {
	t.Helper()
	return newSMTPHarnessTLS(t, back, store, isSubmission, true, configure...)
}

// newCleartextSMTPHarness builds a harness that stops before STARTTLS. The server still
// offers TLS — it is the client that has not upgraded — which is the state a peer is in
// when it connects to port 25.
func newCleartextSMTPHarness(t *testing.T, back *mockBackend, store *mockStore, isSubmission bool, configure ...func(*Server)) *smtpHarness {
	t.Helper()
	return newSMTPHarnessTLS(t, back, store, isSubmission, false, configure...)
}

func newSMTPHarnessTLS(t *testing.T, back *mockBackend, store *mockStore, isSubmission, upgrade bool, configure ...func(*Server)) *smtpHarness {
	t.Helper()
	client, server := net.Pipe()
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 1000})
	// Build the go-smtp server exactly as production does (same construction
	// path), then serve the pipe's server end as a single connection.
	s := NewServer("smtp.test", back, harnessTLSConfig(t), store, limiter)
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
		t:       t,
		back:    back,
		store:   store,
		limiter: limiter,
		conn:    client,
		cr:      bufio.NewReader(client),
		cw:      bufio.NewWriter(client),
		done:    done,
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

	if upgrade {
		h.upgradeTLS(client)
	}

	return h
}

// upgradeTLS runs EHLO, STARTTLS and the handshake, then points the harness at the
// encrypted connection. A second EHLO afterwards — which most tests do — is normal and
// required after an upgrade, since the advertised extensions can change.
func (h *smtpHarness) upgradeTLS(raw net.Conn) {
	h.t.Helper()
	if _, final := h.readReplyAfter("EHLO harness.test"); replyCode(final) != "250" {
		h.t.Fatalf("EHLO before STARTTLS = %q", final)
	}
	if r := h.cmd("STARTTLS"); replyCode(r) != "220" {
		h.t.Fatalf("STARTTLS = %q, want 220 — the server under test should be offering TLS", r)
	}
	tlsConn := tls.Client(raw, &tls.Config{
		ServerName: "smtp.test",
		// The CA is generated per harness and thrown away; verifying against it here
		// would only re-test crypto/tls.
		InsecureSkipVerify: true,
	})
	if err := tlsConn.Handshake(); err != nil {
		h.t.Fatalf("TLS handshake: %v", err)
	}
	h.conn = tlsConn
	h.cr = bufio.NewReader(tlsConn)
	h.cw = bufio.NewWriter(tlsConn)
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
