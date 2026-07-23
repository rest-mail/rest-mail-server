package pop3

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
)

// mockBackend is an in-memory Backend implementation. It lets the transcript
// tests drive the full POP3 state machine — auth, listing, retrieval, deletion —
// with no live API or database.
type mockBackend struct {
	mu sync.Mutex

	user      string
	pass      string
	token     string
	accountID uint

	byFolder map[string][]apiclient.MessageSummary // folder -> summaries (oldest-first)
	details  map[uint]apiclient.MessageDetail
	raws     map[uint]string // stored verbatim RFC 2822; absent -> 404 fallback

	// Recorded side effects, for assertions.
	updates map[uint][]map[string]interface{}
	deletes []uint
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		user:      "alice@example.com",
		pass:      "s3cret",
		token:     "tok-alice",
		accountID: 42,
		byFolder:  map[string][]apiclient.MessageSummary{},
		details:   map[uint]apiclient.MessageDetail{},
		raws:      map[uint]string{},
		updates:   map[uint][]map[string]interface{}{},
	}
}

// seed adds a message to a folder with a stored raw body.
func (m *mockBackend) seed(folder string, id uint, size int, raw string) {
	sum := apiclient.MessageSummary{ID: id, MailboxID: 1, Folder: folder, SizeBytes: size}
	m.byFolder[folder] = append(m.byFolder[folder], sum)
	m.details[id] = apiclient.MessageDetail{MessageSummary: sum}
	if raw != "" {
		m.raws[id] = raw
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

func (m *mockBackend) ListMessages(_ string, _ uint, folder string) (*apiclient.MessageListResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := make([]apiclient.MessageSummary, len(m.byFolder[folder]))
	copy(msgs, m.byFolder[folder])
	return &apiclient.MessageListResponse{Data: msgs}, nil
}

func (m *mockBackend) GetMessage(_ string, msgID uint) (*apiclient.MessageDetailResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.details[msgID]
	if !ok {
		return nil, &apiclient.APIError{StatusCode: 404, Body: "not found"}
	}
	return &apiclient.MessageDetailResponse{Data: d}, nil
}

func (m *mockBackend) GetRawMessage(_ string, msgID uint) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.raws[msgID], nil // absent -> "" -> caller falls back to rebuild
}

func (m *mockBackend) UpdateMessage(_ string, msgID uint, updates map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates[msgID] = append(m.updates[msgID], updates)
	return nil
}

func (m *mockBackend) DeleteMessage(_ string, msgID uint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, msgID)
	return nil
}

func (m *mockBackend) deletedIDs() []uint {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]uint, len(m.deletes))
	copy(out, m.deletes)
	return out
}

func (m *mockBackend) wasMarkedRead(id uint) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.updates[id] {
		if v, ok := u["is_read"].(bool); ok && v {
			return true
		}
	}
	return false
}

// ── Transcript harness ────────────────────────────────────────────────

// pop3Harness drives a real Session over net.Pipe, so tests can script client
// command lines and assert the exact response bytes the server writes back.
type pop3Harness struct {
	t    *testing.T
	mock *mockBackend
	conn net.Conn
	cr   *bufio.Reader
	cw   *bufio.Writer
	done chan struct{}
}

func newPOP3Harness(t *testing.T, mock *mockBackend) *pop3Harness {
	t.Helper()
	client, server := net.Pipe()
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 1000})
	sess := NewSession(server, mock, "pop3.test", nil, limiter)

	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()

	h := &pop3Harness{
		t:    t,
		mock: mock,
		conn: client,
		cr:   bufio.NewReader(client),
		cw:   bufio.NewWriter(client),
		done: done,
	}
	t.Cleanup(func() {
		_ = client.Close()
		<-h.done
	})

	// Consume the greeting.
	if g := h.readLine(); !strings.HasPrefix(g, "+OK") {
		t.Fatalf("greeting = %q, want +OK...", g)
	}
	return h
}

func (h *pop3Harness) readLine() string {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := h.cr.ReadString('\n')
	if err != nil {
		h.t.Fatalf("readLine: %v (partial %q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

// readDotBody reads a multi-line response terminated by a lone ".".
func (h *pop3Harness) readDotBody() []string {
	h.t.Helper()
	var lines []string
	for {
		l := h.readLine()
		if l == "." {
			break
		}
		lines = append(lines, l)
	}
	return lines
}

func (h *pop3Harness) send(format string, args ...interface{}) {
	h.t.Helper()
	_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(h.cw, format+"\r\n", args...)
	if err := h.cw.Flush(); err != nil {
		h.t.Fatalf("send: %v", err)
	}
}

// cmd sends a command and returns the single-line status reply.
func (h *pop3Harness) cmd(format string, args ...interface{}) string {
	h.t.Helper()
	h.send(format, args...)
	return h.readLine()
}

// login runs USER/PASS with the mock's valid credentials.
func (h *pop3Harness) login() {
	h.t.Helper()
	if r := h.cmd("USER %s", h.mock.user); !strings.HasPrefix(r, "+OK") {
		h.t.Fatalf("USER: %q", r)
	}
	if r := h.cmd("PASS %s", h.mock.pass); !strings.HasPrefix(r, "+OK") {
		h.t.Fatalf("PASS: %q", r)
	}
}
