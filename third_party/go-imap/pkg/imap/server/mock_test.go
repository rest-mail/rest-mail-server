package server

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

	"github.com/rest-mail/go-imap/pkg/imap"
)

// mockMailbox is an in-memory [Mailbox]. It lets the transcript tests drive the
// full IMAP state machine — SELECT/FETCH/UID FETCH/STORE/IDLE — with no real
// store.
type mockMailbox struct {
	mu sync.Mutex

	folders  []imap.Folder
	byFolder map[string][]imap.Message // oldest-first
	raws     map[uint32][]byte

	stores  map[uint32][]imap.FlagUpdate
	deletes []uint32
	moves   []moveOp
	copies  []copyOp
	appends []appendOp
}

type moveOp struct {
	uid  uint32
	dest string
}
type copyOp struct {
	uid  uint32
	dest string
}
type appendOp struct {
	dest    string
	flags   imap.FlagUpdate
	date    time.Time // zero unless the client supplied a date-time (DateAppender)
	hasDate bool
	raw     []byte
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
		mbox: &mockMailbox{
			folders:  []imap.Folder{{Name: "INBOX"}},
			byFolder: map[string][]imap.Message{},
			raws:     map[uint32][]byte{},
			stores:   map[uint32][]imap.FlagUpdate{},
		},
	}
}

func (b *mockBackend) seed(folder string, uid uint32, size int, raw string) {
	m := imap.Message{
		UID:     uid,
		Size:    size,
		Subject: "Msg " + strconv.FormatUint(uint64(uid), 10),
		From:    imap.Address{Name: "Sender", Email: "sender@example.com"},
		Date:    time.Date(2025, 3, 15, 10, 0, 0, 0, time.UTC),
	}
	b.mbox.byFolder[folder] = append(b.mbox.byFolder[folder], m)
	if raw != "" {
		b.mbox.raws[uid] = []byte(raw)
	}
}

func (b *mockBackend) Authenticate(user, pass string) (imap.Mailbox, error) {
	if user != b.user || pass != b.pass {
		return nil, fmt.Errorf("invalid credentials")
	}
	return b.mbox, nil
}

func (m *mockMailbox) Folders() ([]imap.Folder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]imap.Folder(nil), m.folders...), nil
}

func (m *mockMailbox) Messages(folder string) ([]imap.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]imap.Message, len(m.byFolder[folder]))
	copy(out, m.byFolder[folder])
	return out, nil
}

func (m *mockMailbox) Fetch(uid uint32) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.raws[uid]
	if !ok {
		return nil, fmt.Errorf("no such message %d", uid)
	}
	return raw, nil
}

func (m *mockMailbox) Store(uid uint32, f imap.FlagUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stores[uid] = append(m.stores[uid], f)
	return nil
}

func (m *mockMailbox) Move(uid uint32, dest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.moves = append(m.moves, moveOp{uid, dest})
	return nil
}

func (m *mockMailbox) Delete(uid uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, uid)
	return nil
}

func (m *mockMailbox) Copy(uid uint32, dest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.copies = append(m.copies, copyOp{uid, dest})
	return nil
}

func (m *mockMailbox) Append(dest string, f imap.FlagUpdate, raw []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appends = append(m.appends, appendOp{dest: dest, flags: f, raw: raw})
	m.growLocked(dest, f, raw)
	return nil
}

// growLocked adds the appended message to the destination folder so a subsequent
// Messages(dest) reflects the new count — letting a test observe the untagged
// "* n EXISTS" a selected-mailbox APPEND must emit (RFC 3501 §6.3.11). It assigns
// the next ascending UID within the folder. Callers hold m.mu.
func (m *mockMailbox) growLocked(dest string, f imap.FlagUpdate, raw []byte) {
	var maxUID uint32
	for _, msg := range m.byFolder[dest] {
		if msg.UID > maxUID {
			maxUID = msg.UID
		}
	}
	msg := imap.Message{UID: maxUID + 1, Size: len(raw)}
	if f.Seen != nil {
		msg.Seen = *f.Seen
	}
	m.byFolder[dest] = append(m.byFolder[dest], msg)
}

// AppendWithDate makes mockMailbox a [DateAppender] so tests can assert that a
// client-supplied APPEND date-time reaches the store. It tracks no UIDs, so it
// returns 0 (no APPENDUID resp-code is expected from this backend).
func (m *mockMailbox) AppendWithDate(dest string, f imap.FlagUpdate, date time.Time, raw []byte) (uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appends = append(m.appends, appendOp{dest: dest, flags: f, date: date, hasDate: true, raw: raw})
	m.growLocked(dest, f, raw)
	return 0, nil
}

// lastAppend returns the most recent APPEND recorded, if any.
func (m *mockMailbox) lastAppend() (appendOp, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.appends) == 0 {
		return appendOp{}, false
	}
	return m.appends[len(m.appends)-1], true
}

func (m *mockMailbox) Quota() (used, limit int64, err error) {
	return 0, 0, nil
}

// wasDeleted reports whether Delete was ever called for uid.
func (m *mockMailbox) wasDeleted(uid uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.deletes {
		if d == uid {
			return true
		}
	}
	return false
}

// lastStore returns the most recent FlagUpdate persisted for uid, if any.
func (m *mockMailbox) lastStore(uid uint32) (imap.FlagUpdate, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fs := m.stores[uid]
	if len(fs) == 0 {
		return imap.FlagUpdate{}, false
	}
	return fs[len(fs)-1], true
}

// wasMarkedRead reports whether any Store for uid set \Seen true.
func (m *mockMailbox) wasMarkedRead(uid uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.stores[uid] {
		if f.Seen != nil && *f.Seen {
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
	user string
	pass string
	conn net.Conn
	cr   *bufio.Reader
	cw   *bufio.Writer
	done chan struct{}

	lastLiteral string // most recent literal payload consumed
}

func newIMAPHarness(t *testing.T, mock *mockBackend) *imapHarness {
	t.Helper()
	h := newIMAPHarnessWith(t, mock, mock.user, mock.pass)
	h.mock = mock
	return h
}

// newIMAPHarnessWith drives a Session over net.Pipe against any Backend, so tests
// can exercise backends beyond the base mockBackend (e.g. one implementing the
// optional UIDPLUS/Mover interfaces). user/pass are the credentials login uses.
func newIMAPHarnessWith(t *testing.T, backend imap.Backend, user, pass string) *imapHarness {
	t.Helper()
	client, server := net.Pipe()
	sess := NewSession(server, backend, "imap.test", nil, imap.NopLimiter{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()

	h := &imapHarness{
		t:    t,
		user: user,
		pass: pass,
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
	_, _ = fmt.Fprintf(h.cw, format+"\r\n", args...)
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
	_, status := h.command(tag, "LOGIN %s %s", h.user, h.pass)
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
