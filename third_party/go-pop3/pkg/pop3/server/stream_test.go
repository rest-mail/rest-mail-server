package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/go-pop3/pkg/pop3"
)

// streamMailbox is a mockMailbox that also opens its messages for streaming, and
// counts what it opened and closed.
type streamMailbox struct {
	*mockMailbox

	mu      sync.Mutex
	opened  int
	closed  int
	openErr error
	// broken, when set, wraps each opened message's ReaderAt.
	broken func(io.ReaderAt) io.ReaderAt
}

var _ pop3.StreamMailbox = (*streamMailbox)(nil)

func (m *streamMailbox) OpenMessage(uid string) (pop3.StoredMessage, error) {
	if m.openErr != nil {
		return nil, m.openErr
	}
	raw, err := m.Retrieve(uid)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.opened++
	m.mu.Unlock()
	var r io.ReaderAt = bytes.NewReader(raw)
	if m.broken != nil {
		r = m.broken(r)
	}
	return &storedMessage{ReaderAt: r, size: int64(len(raw)), m: m}, nil
}

func (m *streamMailbox) counts() (opened, closed int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.opened, m.closed
}

type storedMessage struct {
	io.ReaderAt
	size int64
	m    *streamMailbox
}

func (s *storedMessage) Size() int64 { return s.size }

func (s *storedMessage) Close() error {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	s.m.closed++
	return nil
}

// streamBackend serves a streamMailbox over the mock's maildrop.
type streamBackend struct {
	*mockBackend
	box *streamMailbox
}

func newStreamBackend() *streamBackend {
	mock := newMockBackend()
	return &streamBackend{mockBackend: mock, box: &streamMailbox{mockMailbox: mock.mbox}}
}

func (b *streamBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if _, err := b.mockBackend.Authenticate(user, pass); err != nil {
		return nil, err
	}
	return b.box, nil
}

// readResponse reads one response exactly as sent: its status line and, after +OK,
// every line up to and including the "." terminator.
func (h *pop3Harness) readResponse() string {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var b strings.Builder
	status, err := h.cr.ReadString('\n')
	b.WriteString(status)
	if err != nil {
		h.t.Fatalf("read: %v (partial %q)", err, b.String())
	}
	if !strings.HasPrefix(status, "+OK") {
		return b.String()
	}
	for {
		line, err := h.cr.ReadString('\n')
		b.WriteString(line)
		if err != nil {
			h.t.Fatalf("read: %v (partial %q)", err, b.String())
		}
		if line == ".\r\n" {
			return b.String()
		}
	}
}

// transcript is every response the session sends to RETR and TOP of message 1,
// as sent, followed by LIST.
func transcript(t *testing.T, backend pop3.Backend, mock *mockBackend) string {
	t.Helper()
	h := newPOP3HarnessFor(t, backend, mock)
	h.login()
	var b strings.Builder
	for _, command := range []string{"RETR 1", "TOP 1 0", "TOP 1 1", "TOP 1 2", "TOP 1 100", "RETR 1", "TOP 1", "TOP 2 1", "RETR 2", "TOP 1 -1"} {
		h.send("%s", command)
		fmt.Fprintf(&b, "%s => %s", command, h.readResponse())
	}
	return b.String()
}

var streamedShapes = []string{
	"",
	"Subject: hi\r\n\r\nbody\r\n",
	"Subject: hi\n\nline one\nline two\n",
	"Subject: hi\r\n\r\nline\rone\r\n",
	"Subject: hi\r\n\r\nline\r\r\n",
	"Subject: hi\r\n\r\nline\n\rone\r\n",
	"Subject: hi\r\n\r\nbody",
	"Subject: hi\r\n\r\nbody\r",
	"Subject: hi\r\n",
	"Subject: hi",
	"Subject: hi\r\n\r\n.\r\n..\r\n.leading\r\nplain\r\n",
	"Subject: hi\n\n.\n..\n.leading\nplain\n",
	"Subject: hi\r\n\r\n.last",
	"Subject: Gr\xc3\xbc\xc3\x9fe\r\n\r\n8-bit \xff body\r\n",
	"\r\nno header\r\n",
	"\n",
	"\r",
	"A: 1\r\n\r\n" + strings.Repeat("x", 70000) + "\r\n." + strings.Repeat("y", 40000),
}

// RETR and TOP of a StreamMailbox send exactly the octets they send for the
// same message from Retrieve, and close every message they open.
func TestStreamedRetrAndTopMatchRetrieve(t *testing.T) {
	for _, raw := range streamedShapes {
		plain := newMockBackend()
		plain.seed("u1", pop3.OctetCount([]byte(raw)), raw)
		plain.mbox.raws["u1"] = []byte(raw) // seed skips an empty message
		want := transcript(t, plain, plain)

		streamed := newStreamBackend()
		streamed.seed("u1", pop3.OctetCount([]byte(raw)), raw)
		streamed.mbox.raws["u1"] = []byte(raw)
		if got := transcript(t, streamed, streamed.mockBackend); got != want {
			t.Fatalf("stored %q:\nstreamed %q\nwant     %q", raw, got, want)
		}
		opened, closed := streamed.box.counts()
		if opened != 6 || closed != opened {
			t.Fatalf("stored %q: opened %d messages and closed %d; want 6 and 6", raw, opened, closed)
		}
		if !streamed.mbox.wasMarkedSeen("u1") {
			t.Fatalf("stored %q: RETR did not mark the message seen", raw)
		}
	}
}

type utf8StreamBackend struct{ *streamBackend }

func (b utf8StreamBackend) AuthenticateUTF8(user, pass string, _ bool) (pop3.Mailbox, error) {
	return b.Authenticate(user, pass)
}

// failingRetrieve fails Retrieve, which is how the plain mailbox reports what
// OpenMessage reports for the streamed one.
type failingRetrieve struct {
	*mockMailbox
	err error
}

func (m failingRetrieve) Retrieve(string) ([]byte, error) { return nil, m.err }

type failingPlainBackend struct {
	*mockBackend
	err error
}

func (b failingPlainBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if _, err := b.mockBackend.Authenticate(user, pass); err != nil {
		return nil, err
	}
	return failingRetrieve{b.mbox, b.err}, nil
}

func (b failingPlainBackend) AuthenticateUTF8(user, pass string, _ bool) (pop3.Mailbox, error) {
	return b.Authenticate(user, pass)
}

// A message OpenMessage cannot open gets the reply Retrieve's error gets, and
// is not marked seen.
func TestStreamedOpenErrorsMatchRetrieve(t *testing.T) {
	for _, err := range []error{errors.New("store down"), pop3.ErrUTF8Required, fmt.Errorf("wrapped: %w", pop3.ErrUTF8Required)} {
		plain := newMockBackend()
		plain.seed("u1", 3, "x\r\n")
		want := transcript(t, failingPlainBackend{plain, err}, plain)

		streamed := newStreamBackend()
		streamed.seed("u1", 3, "x\r\n")
		streamed.box.openErr = err
		if got := transcript(t, utf8StreamBackend{streamed}, streamed.mockBackend); got != want {
			t.Fatalf("%v:\nstreamed %q\nwant     %q", err, got, want)
		}
		if streamed.mbox.wasMarkedSeen("u1") {
			t.Fatalf("%v: a message that could not be opened was marked seen", err)
		}
	}
	if !strings.Contains(transcript(t, func() pop3.Backend {
		b := newStreamBackend()
		b.seed("u1", 3, "x\r\n")
		b.box.openErr = pop3.ErrUTF8Required
		return utf8StreamBackend{b}
	}(), newMockBackend()), "-ERR [UTF8] Message requires UTF-8 mode") {
		t.Fatal("ErrUTF8Required from OpenMessage did not get [UTF8]")
	}
}

// failAfter is a ReaderAt that fails once a read reaches offset at, or panics
// there when panics is set.
type failAfter struct {
	r      io.ReaderAt
	at     int64
	panics bool
	reads  *int
}

var errDisk = errors.New("disk failed")

func (f failAfter) ReadAt(p []byte, off int64) (int, error) {
	if f.reads != nil {
		*f.reads++
		if *f.reads == 1 {
			// The count reads it whole; only the send fails.
			return f.r.ReadAt(p, off)
		}
	}
	if off+int64(len(p)) > f.at {
		if f.panics {
			panic("store exploded")
		}
		n, _ := f.r.ReadAt(p[:max(0, f.at-off)], off)
		return n, errDisk
	}
	return f.r.ReadAt(p, off)
}

// A message that cannot be read while RETR counts it gets -ERR and stays
// unseen; one that fails after "+OK", while RETR or TOP sends it, ends the
// connection with no "." terminator, so the client cannot take a part for the
// whole. Either way the message is closed.
func TestStreamedReadErrors(t *testing.T) {
	raw := "Subject: hi\r\n\r\n" + strings.Repeat("body line\r\n", 100)

	b := newStreamBackend()
	b.seed("u1", pop3.OctetCount([]byte(raw)), raw)
	b.box.broken = func(r io.ReaderAt) io.ReaderAt { return failAfter{r: r, at: 20} }
	h := newPOP3HarnessFor(t, b, b.mockBackend)
	h.login()
	if got := h.cmd("RETR 1"); got != "-ERR Failed to retrieve message" {
		t.Fatalf("RETR of an unreadable message = %q", got)
	}
	if b.mbox.wasMarkedSeen("u1") {
		t.Fatal("an unreadable message was marked seen")
	}
	if got := h.cmd("NOOP"); got != "+OK" {
		t.Fatalf("after a refused RETR, NOOP = %q", got)
	}

	for _, command := range []string{"RETR 1", "TOP 1 50"} {
		b := newStreamBackend()
		b.seed("u1", pop3.OctetCount([]byte(raw)), raw)
		b.box.broken = func(r io.ReaderAt) io.ReaderAt { return failAfter{r: r, at: 200, reads: new(int)} }
		if command == "TOP 1 50" {
			b.box.broken = func(r io.ReaderAt) io.ReaderAt { return failAfter{r: r, at: 200} }
		}
		h := newPOP3HarnessFor(t, b, b.mockBackend)
		h.login()
		h.send("%s", command)
		got := readUntilClosed(t, h)
		if !strings.HasPrefix(got, "+OK") || strings.HasSuffix(got, "\r\n.\r\n") {
			t.Fatalf("%s failing after +OK sent %q; want +OK and no terminator", command, got)
		}
		<-h.done
		if opened, closed := b.box.counts(); opened != 1 || closed != 1 {
			t.Fatalf("%s: opened %d, closed %d", command, opened, closed)
		}
		if b.mbox.wasMarkedSeen("u1") {
			t.Fatalf("%s: a message cut off was marked seen", command)
		}
	}
}

func readUntilClosed(t *testing.T, h *pop3Harness) string {
	t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := io.ReadAll(h.cr)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read: %v", err)
	}
	return string(got)
}

// A panic while a message is sent closes the message, and the session recovers
// it as it recovers any other.
func TestStreamedPanicClosesTheMessage(t *testing.T) {
	for _, command := range []string{"RETR 1", "TOP 1 5"} {
		b := newStreamBackend()
		b.seed("u1", 5, "abc\r\n")
		b.box.broken = func(r io.ReaderAt) io.ReaderAt { return failAfter{r: r, at: 0, panics: true} }
		h := newPOP3HarnessFor(t, b, b.mockBackend)
		h.login()
		h.send("%s", command)
		got := readUntilClosed(t, h)
		if !strings.Contains(got, "-ERR Internal server error") {
			t.Fatalf("%s panicking sent %q", command, got)
		}
		<-h.done
		if opened, closed := b.box.counts(); opened != 1 || closed != 1 {
			t.Fatalf("%s: opened %d, closed %d", command, opened, closed)
		}
	}
}

// A client that goes away during a RETR leaves no message open.
func TestStreamedDisconnectClosesTheMessage(t *testing.T) {
	raw := "Subject: big\r\n\r\n" + strings.Repeat("a long line of the message body\r\n", 1<<15)
	b := newStreamBackend()
	b.seed("u1", pop3.OctetCount([]byte(raw)), raw)
	h := newPOP3HarnessFor(t, b, b.mockBackend)
	h.login()
	h.send("RETR 1")
	if got := h.readLine(); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("RETR = %q", got)
	}
	_ = h.conn.Close()
	select {
	case <-h.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end when its client went away")
	}
	if opened, closed := b.box.counts(); opened != 1 || closed != 1 {
		t.Fatalf("opened %d, closed %d", opened, closed)
	}
}

// closeErrMailbox fails Close, which is logged and changes nothing on the wire.
type closeErrMessage struct{ pop3.StoredMessage }

func (closeErrMessage) Close() error { return errors.New("cannot remove") }

type closeErrMailbox struct{ *streamMailbox }

func (m closeErrMailbox) OpenMessage(uid string) (pop3.StoredMessage, error) {
	msg, err := m.streamMailbox.OpenMessage(uid)
	return closeErrMessage{msg}, err
}

type closeErrBackend struct{ *streamBackend }

func (b closeErrBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if _, err := b.streamBackend.Authenticate(user, pass); err != nil {
		return nil, err
	}
	return closeErrMailbox{b.box}, nil
}

func TestStreamedCloseErrorIsOnlyLogged(t *testing.T) {
	raw := "Subject: hi\r\n\r\nbody\r\n"
	plain := newMockBackend()
	plain.seed("u1", pop3.OctetCount([]byte(raw)), raw)
	want := transcript(t, plain, plain)

	b := newStreamBackend()
	b.seed("u1", pop3.OctetCount([]byte(raw)), raw)
	if got := transcript(t, closeErrBackend{b}, b.mockBackend); got != want {
		t.Fatalf("streamed %q\nwant     %q", got, want)
	}
}

// changingMessage grows between RETR's count and its send.
type changingMessage struct {
	pop3.StoredMessage
	reads int
}

func (c *changingMessage) Size() int64 {
	c.reads++
	if c.reads > 1 {
		return c.StoredMessage.Size() - 3
	}
	return c.StoredMessage.Size()
}

type changingMailbox struct{ *streamMailbox }

func (m changingMailbox) OpenMessage(uid string) (pop3.StoredMessage, error) {
	msg, err := m.streamMailbox.OpenMessage(uid)
	return &changingMessage{StoredMessage: msg}, err
}

type changingBackend struct{ *streamBackend }

func (b changingBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if _, err := b.streamBackend.Authenticate(user, pass); err != nil {
		return nil, err
	}
	return changingMailbox{b.box}, nil
}

// A message whose octets change between RETR's count and its send is cut off,
// because the count already went out.
func TestStreamedRetrRefusesAChangedMessage(t *testing.T) {
	raw := "Subject: hi\r\n\r\nbody\r\n"
	b := newStreamBackend()
	b.seed("u1", pop3.OctetCount([]byte(raw)), raw)
	h := newPOP3HarnessFor(t, changingBackend{b}, b.mockBackend)
	h.login()
	h.send("RETR 1")
	got := readUntilClosed(t, h)
	if !strings.HasPrefix(got, "+OK 21 octets\r\n") || strings.HasSuffix(got, "\r\n.\r\n") {
		t.Fatalf("RETR of a changing message sent %q", got)
	}
	<-h.done
	if opened, closed := b.box.counts(); opened != 1 || closed != 1 {
		t.Fatalf("opened %d, closed %d", opened, closed)
	}
}
