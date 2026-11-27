package server

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/go-pop3/pkg/pop3"
)

// mockMailbox is an in-memory [pop3.Mailbox]. It lets the transcript tests drive the
// full POP3 state machine — listing, retrieval, deletion — with no real store.
type mockMailbox struct {
	mu sync.Mutex

	msgs []pop3.Message
	raws map[string][]byte // UID -> stored verbatim RFC 5322 bytes

	// Recorded side effects, for assertions.
	seen    []string
	deletes []string

	// deleteErr, when non-nil, makes Delete fail (simulating a backend whose
	// UPDATE-phase expunge could not commit). The UID is still recorded so tests
	// can assert Delete was attempted.
	deleteErr error
}

// mockBackend authenticates one user and hands out a shared mockMailbox.
type mockBackend struct {
	user string
	pass string
	mbox *mockMailbox
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		user: "alice@example.com",
		pass: "s3cret",
		mbox: &mockMailbox{raws: map[string][]byte{}},
	}
}

// seed adds a message to the maildrop with a stored raw body.
func (m *mockBackend) seed(uid string, size int, raw string) {
	m.mbox.msgs = append(m.mbox.msgs, pop3.Message{UID: uid, Size: size})
	if raw != "" {
		m.mbox.raws[uid] = []byte(raw)
	}
}

func (m *mockBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if user != m.user || pass != m.pass {
		return nil, errors.New("invalid credentials")
	}
	return m.mbox, nil
}

func (m *mockMailbox) Messages() ([]pop3.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]pop3.Message, len(m.msgs))
	copy(out, m.msgs)
	return out, nil
}

func (m *mockMailbox) Retrieve(uid string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.raws[uid]
	if !ok {
		return nil, fmt.Errorf("no such message %q", uid)
	}
	return raw, nil
}

func (m *mockMailbox) MarkSeen(uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, uid)
	return nil
}

func (m *mockMailbox) Delete(uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, uid)
	return m.deleteErr
}

// failDelete arms the mailbox so subsequent Delete calls return err.
func (m *mockMailbox) failDelete(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteErr = err
}

func (m *mockMailbox) deletedUIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.deletes))
	copy(out, m.deletes)
	return out
}

func (m *mockMailbox) wasMarkedSeen(uid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.seen {
		if u == uid {
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
	return newPOP3HarnessFor(t, mock, mock)
}

// newPOP3HarnessFor serves backend, which wraps mock for the login helper.
func newPOP3HarnessFor(t *testing.T, backend pop3.Backend, mock *mockBackend) *pop3Harness {
	t.Helper()
	client, server := net.Pipe()
	sess := NewSession(server, backend, nil, pop3.NopLimiter{})

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
	_, _ = fmt.Fprintf(h.cw, format+"\r\n", args...)
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
