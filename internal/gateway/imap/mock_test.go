package imap

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
)

// mockBackend is an in-memory Backend implementation. It lets the transcript
// tests drive the full IMAP state machine — SELECT/FETCH/UID FETCH/STORE/IDLE —
// with no live API or database.
type mockBackend struct {
	mu sync.Mutex

	user      string
	pass      string
	token     string
	accountID uint

	folders  []apiclient.Folder
	byFolder map[string][]apiclient.MessageSummary // oldest-first
	details  map[uint]apiclient.MessageDetail
	raws     map[uint]string

	updates map[uint][]map[string]interface{}
	deletes []uint
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		user:      "alice@example.com",
		pass:      "s3cret",
		token:     "tok-alice",
		accountID: 42,
		folders:   []apiclient.Folder{{Name: "INBOX"}},
		byFolder:  map[string][]apiclient.MessageSummary{},
		details:   map[uint]apiclient.MessageDetail{},
		raws:      map[uint]string{},
		updates:   map[uint][]map[string]interface{}{},
	}
}

func (m *mockBackend) seed(folder string, id uint, size int, raw string) {
	sum := apiclient.MessageSummary{ID: id, MailboxID: 1, Folder: folder, SizeBytes: size,
		Sender: "sender@example.com", SenderName: "Sender", Subject: "Msg " + strconv.FormatUint(uint64(id), 10),
		ReceivedAt: time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC)}
	m.byFolder[folder] = append(m.byFolder[folder], sum)
	m.details[id] = apiclient.MessageDetail{MessageSummary: sum, BodyText: "body of " + strconv.FormatUint(uint64(id), 10)}
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

func (m *mockBackend) ListFolders(_ string, _ uint) (*apiclient.FolderListResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &apiclient.FolderListResponse{Data: append([]apiclient.Folder(nil), m.folders...)}, nil
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
	return m.raws[msgID], nil
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

func (m *mockBackend) DeliverMessage(req *apiclient.DeliverRequest) (*apiclient.DeliverResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	resp := &apiclient.DeliverResponse{}
	resp.Data.ID = 1000 + uint(len(m.details))
	resp.Data.MailboxID = req.MailboxID
	resp.Data.Subject = req.Subject
	return resp, nil
}

func (m *mockBackend) GetQuota(_ string, _ uint) (*apiclient.QuotaResponse, error) {
	return &apiclient.QuotaResponse{}, nil
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

// imapHarness drives a real Session over net.Pipe. It understands IMAP literals
// ({N} octet counts), so a caller can consume BODY[] responses and reach the
// tagged completion line.
type imapHarness struct {
	t    *testing.T
	mock *mockBackend
	conn net.Conn
	cr   *bufio.Reader
	cw   *bufio.Writer
	done chan struct{}

	lastLiteral string // most recent literal payload consumed
}

func newIMAPHarness(t *testing.T, mock *mockBackend) *imapHarness {
	t.Helper()
	client, server := net.Pipe()
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 1000})
	sess := NewSession(server, mock, "imap.test", nil, limiter)

	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()

	h := &imapHarness{
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
	if g := h.readLine(); !strings.HasPrefix(g, "* OK") {
		t.Fatalf("greeting = %q, want * OK...", g)
	}
	return h
}

func (h *imapHarness) readLine() string {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := h.cr.ReadString('\n')
	if err != nil {
		h.t.Fatalf("readLine: %v (partial %q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

func (h *imapHarness) readN(n int) string {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(h.cr, buf); err != nil {
		h.t.Fatalf("readN(%d): %v", n, err)
	}
	return string(buf)
}

func (h *imapHarness) send(format string, args ...interface{}) {
	h.t.Helper()
	_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(h.cw, format+"\r\n", args...)
	if err := h.cw.Flush(); err != nil {
		h.t.Fatalf("send: %v", err)
	}
}

// command sends "<tag> <line>" and reads the whole response, returning the
// untagged lines and the final tagged status line. Literals are consumed so the
// tagged line is always reached.
func (h *imapHarness) command(tag, format string, args ...interface{}) (untagged []string, status string) {
	h.t.Helper()
	h.send("%s %s", tag, fmt.Sprintf(format, args...))
	for {
		line := h.readLine()
		if lit, n, ok := literalSuffix(line); ok {
			h.lastLiteral = h.readN(n)
			rest := h.readLine() // trailing ")" after the literal payload
			untagged = append(untagged, lit+" <"+strconv.Itoa(n)+" octets> "+rest)
			continue
		}
		if strings.HasPrefix(line, tag+" ") {
			return untagged, line
		}
		untagged = append(untagged, line)
	}
}

// literalSuffix reports whether an untagged line ends with an IMAP literal
// count "{N}" and returns the line and N.
func literalSuffix(line string) (string, int, bool) {
	if !strings.HasSuffix(line, "}") {
		return "", 0, false
	}
	open := strings.LastIndex(line, "{")
	if open < 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(line[open+1 : len(line)-1])
	if err != nil {
		return "", 0, false
	}
	return line, n, true
}

func (h *imapHarness) login(tag string) {
	h.t.Helper()
	_, status := h.command(tag, "LOGIN %s %s", h.mock.user, h.mock.pass)
	if !strings.Contains(status, " OK") {
		h.t.Fatalf("LOGIN status = %q", status)
	}
}

func (h *imapHarness) selectInbox(tag string) {
	h.t.Helper()
	_, status := h.command(tag, "SELECT INBOX")
	if !strings.Contains(status, " OK") {
		h.t.Fatalf("SELECT status = %q", status)
	}
}
