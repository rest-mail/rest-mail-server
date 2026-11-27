package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// fetchStreamMailbox is mockMailbox as an [imap.StreamFetcher]: FetchStream opens the
// message open makes, and the counters say which way FETCH read it.
type fetchStreamMailbox struct {
	*mockMailbox
	open     func(raw []byte) (imap.FetchedMessage, error)
	fetches  atomic.Int32 // Fetch calls
	streams  atomic.Int32 // FetchStream calls
	opened   atomic.Int32 // FetchedMessages not yet closed
	storeErr error
}

func (m *fetchStreamMailbox) Fetch(uid uint32) ([]byte, error) {
	m.fetches.Add(1)
	return m.mockMailbox.Fetch(uid)
}

func (m *fetchStreamMailbox) FetchStream(uid uint32) (imap.FetchedMessage, error) {
	m.streams.Add(1)
	raw, err := m.mockMailbox.Fetch(uid)
	if err != nil {
		return nil, err
	}
	open := m.open
	if open == nil {
		open = func(raw []byte) (imap.FetchedMessage, error) { return &memMessage{Reader: bytes.NewReader(raw)}, nil }
	}
	msg, err := open(raw)
	if err != nil {
		return nil, err
	}
	m.opened.Add(1)
	return &countedMessage{FetchedMessage: msg, open: &m.opened}, nil
}

func (m *fetchStreamMailbox) Store(uid uint32, f imap.FlagUpdate) error {
	if m.storeErr != nil {
		return m.storeErr
	}
	return m.mockMailbox.Store(uid, f)
}

type memMessage struct{ *bytes.Reader }

func (m *memMessage) Close() error { return nil }

type countedMessage struct {
	imap.FetchedMessage
	open   *atomic.Int32
	closed atomic.Bool
}

func (c *countedMessage) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		c.open.Add(-1)
	}
	return c.FetchedMessage.Close()
}

type streamBackend struct {
	*mockBackend
	mbox imap.Mailbox
}

func (b *streamBackend) Authenticate(user, pass string) (imap.Mailbox, error) {
	if _, err := b.mockBackend.Authenticate(user, pass); err != nil {
		return nil, err
	}
	return b.mbox, nil
}

// fetchTranscript is every octet the server sends for one FETCH command, from
// a fresh session on a fresh store holding raw as UID 7, through the tagged
// completion. The store streams when stream is set.
func fetchTranscript(t *testing.T, raw, items string, utf8, stream bool) (string, *fetchStreamMailbox) {
	t.Helper()
	mock := newMockBackend()
	mock.seed("INBOX", 7, len(raw), raw)
	sm := &fetchStreamMailbox{mockMailbox: mock.mbox}
	var backend imap.Backend = mock
	if stream {
		backend = &streamBackend{mockBackend: mock, mbox: sm}
	}
	h := newIMAPHarnessSession(t, backend, mock.user, mock.pass, func(s *Session) { s.utf8 = utf8 })
	h.login("a1")
	h.selectInbox("a2")
	h.send("a3 UID FETCH 7 %s", items)
	return h.readThrough("a3"), sm
}

// readThrough is every octet up to and including the line tagged tag, literals
// taken by their counts.
func (h *imapHarness) readThrough(tag string) string {
	h.t.Helper()
	var b strings.Builder
	for {
		line := h.readLine()
		b.WriteString(line + "\r\n")
		for {
			_, n, ok := literalSuffix(line)
			if !ok {
				break
			}
			b.WriteString(h.readN(n))
			line = h.readLine()
			b.WriteString(line + "\r\n")
		}
		if strings.HasPrefix(line, tag+" ") {
			return b.String()
		}
	}
}

// fetchShapes are stored messages FETCH sends as stored and ones it converts.
var fetchShapes = map[string]string{
	"7-bit":                            "From: a@example.test\r\nSubject: hi\r\n\r\nline one\r\nline two\r\n",
	"bare LF stored":                   "From: a@example.test\nSubject: lf\n\nbody\nmore\n",
	"mixed endings":                    "Subject: mixed\r\nX-A: b\n\r\nbody\r\nbare\nend\r",
	"bare LF header, CRLF blank later": "Subject: x\n\nfirst\r\n\r\nsecond\r\n",
	"no header":                        "\r\n\r\nbody only\r\n",
	"no blank line":                    "Subject: only a header\r\nX-B: c\r\n",
	"8-bit text":                       "Subject: x\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nJ\xc3\xb6rg\r\n",
	"8-bit header":                     "Subject: Gr\xc3\xbc\xc3\x9fe\r\n\r\nplain\r\n",
	"8-bit no charset":                 "Subject: x\r\n\r\ncaf\xe9\r\n",
	"NUL in text":                      "Subject: x\r\n\r\na\x00b\r\n",
	"UTF-8 no charset":                 "Subject: x\r\n\r\ncaf\xc3\xa9\r\n",
	"UTF-8 split across reads":         "Subject: x\r\n\r\n" + strings.Repeat("a", 4095) + "é\x00\xef\xbf\xbd",
	"truncated UTF-8":                  "Subject: x\r\n\r\n" + strings.Repeat("a", 4095) + "\xc3",
	"mixed charsets and NUL":           "Subject: Grüße\x00\r\nContent-Type: text/plain; charset=iso-8859-1\r\n\r\ncaf\xe9\x00\r\n",
	"wide text with NUL as data":       "Content-Type: text/plain; charset=utf-16le\r\n\r\na\x00\xe9\x00\r\n",
	"header NUL, clean binary body":    "Subject: ab\x00cd\r\nContent-Type: application/octet-stream\r\n\r\nabc\r\n",
	"8-bit bare LF header":             "Subject: Grüße\n\nfirst\r\n\r\nsecond\x00\r\n",
	"8-bit bare LF without CRLF":       "Subject: Grüße\n\ncafé\x00\n",
	"encoded 8-bit boundary":           "Content-Type: multipart/mixed; boundary*=utf-8''%C3%A9\r\nX-Note: é\r\n\r\nplain preamble\r\n",
	"nested text with NUL": "Content-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: message/rfc822\r\n\r\nSubject: Grüße\r\nContent-Type: text/plain; charset=utf-8\r\n\r\ncafé\x00\r\n--B--\r\n",
	"nested parts": "Subject: n\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nhello\r\n" +
		"--B\r\nContent-Type: message/rfc822\r\n\r\nSubject: inner\r\nContent-Type: multipart/alternative; boundary=C\r\n\r\n" +
		"--C\r\nContent-Type: text/plain\r\n\r\ninner text\r\n--C--\r\n" +
		"--B\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: binary\r\n\r\n\xff\x00\x01\r\n" +
		"--B--\r\n",
	"nested 7-bit parts": "Subject: n\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
		"--B\r\nContent-Type: text/plain\r\n\r\nhello\r\n" +
		"--B\r\nContent-Type: message/rfc822\r\n\r\nSubject: inner\r\n\r\ninner\r\n" +
		"--B--\r\n",
}

// fetchItemLists are FETCH item lists a span response answers, and ones it
// leaves to the whole message.
var fetchItemLists = []string{
	"BODY[]", "BODY.PEEK[]", "RFC822", "RFC822.HEADER", "RFC822.TEXT",
	"BODY.PEEK[HEADER]", "BODY[TEXT]", "BODY.PEEK[TEXT]", "BINARY[]", "BINARY.PEEK[]", "BINARY.SIZE[]",
	"BODY.PEEK[]<0.10>", "BODY.PEEK[]<5.100000>", "BODY.PEEK[]<100000000.5>", "BODY.PEEK[TEXT]<3.7>",
	"BODY.PEEK[HEADER]<2.5>", "BINARY.PEEK[]<1.3>", "BINARY.PEEK[]<0.40>", "RFC822.SIZE",
	"(UID FLAGS RFC822.SIZE INTERNALDATE BODY.PEEK[] BODY.PEEK[TEXT]<1.4> BINARY.SIZE[] RFC822.HEADER)",
	"(BODY.PEEK[HEADER] BINARY.PEEK[] BODY.PEEK[])",
	"(ENVELOPE BODY.PEEK[])", "(BODYSTRUCTURE BODY.PEEK[])", "(BODY.PEEK[1] BODY.PEEK[])",
	"(BODY.PEEK[HEADER.FIELDS (SUBJECT)] BODY.PEEK[])", "(BINARY.PEEK[1] BINARY.PEEK[])",
}

// A store that streams gets the same FETCH responses, octet for octet, as one
// that hands the message over whole, across message shapes, item lists and both
// kinds of session (#2107).
func TestStreamedFetchSendsTheSameOctets(t *testing.T) {
	for shape, raw := range fetchShapes {
		for _, utf8 := range []bool{false, true} {
			for _, items := range fetchItemLists {
				name := fmt.Sprintf("%s/utf8=%v/%s", shape, utf8, items)
				want, _ := fetchTranscript(t, raw, items, utf8, false)
				got, sm := fetchTranscript(t, raw, items, utf8, true)
				if got != want {
					t.Errorf("%s:\n got %q\nwant %q", name, got, want)
				}
				if sm.opened.Load() != 0 {
					t.Errorf("%s: %d streamed messages left open", name, sm.opened.Load())
				}
				if n := sm.fetches.Load() + sm.streams.Load(); n > 1 {
					t.Errorf("%s: the store was read %d times", name, n)
				}
			}
		}
	}
}

// A streamable item opens the source once, even when wire conversion needs the
// whole message. Items requiring a MIME parse go directly through Fetch.
func TestStreamedFetchReadsTheStreamNotFetch(t *testing.T) {
	for _, tc := range []struct {
		shape, items     string
		streams, fetches int32
	}{
		{"7-bit", "BODY.PEEK[]", 1, 0},
		{"8-bit text", "BODY.PEEK[]", 1, 0},
		{"8-bit text", "BINARY.PEEK[]", 1, 0},
		{"7-bit", "(ENVELOPE BODY.PEEK[])", 0, 1},
		{"7-bit", "(FLAGS RFC822.SIZE)", 0, 0},
	} {
		_, sm := fetchTranscript(t, fetchShapes[tc.shape], tc.items, false, true)
		if sm.streams.Load() != tc.streams || sm.fetches.Load() != tc.fetches {
			t.Errorf("%s %s: FetchStream %d, Fetch %d; want %d, %d", tc.shape, tc.items,
				sm.streams.Load(), sm.fetches.Load(), tc.streams, tc.fetches)
		}
	}
}

// fileMessage is a message in a file, as a store spools one.
type fileMessage struct {
	*os.File
	size int64
}

func (f *fileMessage) Size() int64 { return f.size }

func (f *fileMessage) Close() error {
	err := f.File.Close()
	_ = os.Remove(f.Name())
	return err
}

// spoolTo is a FetchStream opener that writes the message to a file in dir.
func spoolTo(dir string) func([]byte) (imap.FetchedMessage, error) {
	return func(raw []byte) (imap.FetchedMessage, error) {
		f, err := os.CreateTemp(dir, "msg-*")
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(raw); err != nil {
			return nil, err
		}
		return &fileMessage{File: f, size: int64(len(raw))}, nil
	}
}

// bigFetchMessage is a message of about 9 MiB with a repeated body line.
func bigFetchMessage(header, line string) string {
	var b strings.Builder
	b.Grow(9<<20 + len(line))
	b.WriteString(header)
	for b.Len() < 9<<20 {
		b.WriteString(line)
	}
	return b.String()
}

// FETCH BODY[] of a 9 MiB message from a store that streams it allocates a small
// fraction of the message: the literal is copied from the store's file through
// the connection's buffer (#2107 AC01).
func TestStreamedFetchOfABigMessageHoldsLittleOfIt(t *testing.T) {
	for _, tc := range []struct {
		name, header, line string
		utf8               bool
	}{
		{"seven-bit", "Subject: big\r\n\r\n", strings.Repeat("0123456789abcdef", 4) + "\r\n", false},
		{"legacy-charset", "Subject: big\r\nContent-Type: text/plain; charset=iso-8859-1\r\n\r\n", "café\xe9\x00\r\n", false},
		{"utf8-default", "Subject: Grüße\r\n\r\n", "café\x00\r\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkBigStreamedFetch(t, bigFetchMessage(tc.header, tc.line), tc.utf8)
		})
	}
}

func checkBigStreamedFetch(t *testing.T, raw string, utf8 bool) {
	t.Helper()
	mock := newMockBackend()
	mock.seed("INBOX", 7, len(raw), raw)
	// Fetch is the store's file read whole, as a store with no StreamFetcher has
	// to; FetchStream hands over the file.
	dir := t.TempDir()
	sm := &fetchStreamMailbox{mockMailbox: mock.mbox, open: spoolTo(dir)}
	h := newIMAPHarnessSession(t, &streamBackend{mockBackend: mock, mbox: sm}, mock.user, mock.pass, func(s *Session) { s.utf8 = utf8 })
	h.login("a1")
	h.selectInbox("a2")

	const textPartial = "BODY.PEEK[TEXT]<0.8000000>"
	for _, items := range []string{"BODY.PEEK[]", "BINARY.PEEK[]", "BODY[]", "RFC822", textPartial} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		h.send("f UID FETCH 7 %s", items)
		n := discardThrough(t, h, "f")
		runtime.ReadMemStats(&after)
		want := len(raw)
		if items == textPartial {
			want = 8_000_000
		}
		if n != want {
			t.Errorf("FETCH %s sent %d literal octets, want %d", items, n, want)
		}
		got := after.TotalAlloc - before.TotalAlloc
		t.Logf("FETCH %s of %d octets allocated %d", items, len(raw), got)
		if got > uint64(len(raw))/16 {
			t.Errorf("FETCH %s of %d octets allocated %d", items, len(raw), got)
		}
	}
	if sm.fetches.Load() != 0 {
		t.Errorf("Fetch called %d times", sm.fetches.Load())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 || sm.opened.Load() != 0 {
		t.Errorf("left open %d, files %v", sm.opened.Load(), left)
	}
}

// discardThrough reads the server's octets up to the line tagged tag, literals
// through a small buffer, so the reading allocates nothing that grows with them.
func discardThrough(t *testing.T, h *imapHarness, tag string) int {
	t.Helper()
	total := 0
	for {
		line := h.readLine()
		for {
			_, n, ok := literalSuffix(line)
			if !ok {
				break
			}
			if _, err := io.CopyN(io.Discard, h.cr, int64(n)); err != nil {
				t.Fatal(err)
			}
			total += n
			line = h.readLine()
		}
		if strings.HasPrefix(line, tag+" ") {
			if !strings.Contains(line, " OK") {
				t.Fatalf("FETCH: %q", line)
			}
			return total
		}
	}
}

// failingAt is a message whose reads fail from octet at on.
type failingAt struct {
	imap.FetchedMessage
	at int64
}

func (f *failingAt) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.at {
		if off >= f.at {
			return 0, errors.New("disk gone")
		}
		n, _ := f.FetchedMessage.ReadAt(p[:f.at-off], off)
		return n, errors.New("disk gone")
	}
	return f.FetchedMessage.ReadAt(p, off)
}

// longerSize says the message is longer than the octets it holds.
type longerSize struct{ imap.FetchedMessage }

func (l *longerSize) Size() int64 { return l.FetchedMessage.Size() + 10 }

// panicStore panics in Store, the \Seen a non-peek FETCH sets.
type panicStore struct{ *fetchStreamMailbox }

func (p *panicStore) Store(uint32, imap.FlagUpdate) error { panic("store bug") }

// The message a store opened for FETCH is closed, and its file gone, however the
// response ends: sent, refused before sending, cut off by a read that fails in
// the middle, the client leaving, or a panic (#2107).
func TestStreamedFetchClosesTheMessageHoweverItEnds(t *testing.T) {
	raw := bigFetchMessage("Subject: big\r\n\r\n", strings.Repeat("0123456789abcdef", 4)+"\r\n")
	type outcome struct {
		name  string
		setup func(sm *fetchStreamMailbox, dir string)
		mbox  func(sm *fetchStreamMailbox) imap.Mailbox
		run   func(t *testing.T, h *imapHarness)
	}
	expectTagged := func(want string) func(t *testing.T, h *imapHarness) {
		return func(t *testing.T, h *imapHarness) {
			_, status := h.command("f", "UID FETCH 7 BODY[]")
			if !strings.Contains(status, want) {
				t.Fatalf("status %q, want %q", status, want)
			}
		}
	}
	expectClosed := func(t *testing.T, h *imapHarness) {
		h.send("f UID FETCH 7 BODY.PEEK[]")
		_ = h.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, err := io.Copy(io.Discard, h.cr)
		if err != nil {
			t.Fatalf("connection: %v", err)
		}
	}
	for _, tc := range []outcome{
		{name: "sent", run: expectTagged("f OK")},
		{name: "store refuses \\Seen", setup: func(sm *fetchStreamMailbox, _ string) { sm.storeErr = errors.New("no") }, run: expectTagged("f NO")},
		{name: "scan read fails", setup: func(sm *fetchStreamMailbox, dir string) {
			open := spoolTo(dir)
			sm.open = func(b []byte) (imap.FetchedMessage, error) {
				m, err := open(b)
				return &failingAt{FetchedMessage: m, at: 1 << 20}, err
			}
		}, run: expectTagged("f NO")},
		{name: "message shorter than its size", setup: func(sm *fetchStreamMailbox, dir string) {
			open := spoolTo(dir)
			sm.open = func(b []byte) (imap.FetchedMessage, error) {
				m, err := open(b)
				return &longerSize{FetchedMessage: m}, err
			}
		}, run: expectTagged("f NO")},
		{name: "read fails mid-literal", setup: func(sm *fetchStreamMailbox, dir string) {
			open := spoolTo(dir)
			calls := 0
			sm.open = func(b []byte) (imap.FetchedMessage, error) {
				m, err := open(b)
				return &midCopyFailure{FetchedMessage: m, calls: &calls}, err
			}
		}, run: expectClosed},
		{name: "client leaves mid-literal", run: func(t *testing.T, h *imapHarness) {
			h.send("f UID FETCH 7 BODY.PEEK[]")
			_ = h.readLine()
			_, _ = io.CopyN(io.Discard, h.cr, 1<<20)
			_ = h.conn.Close()
		}},
		{name: "panic", mbox: func(sm *fetchStreamMailbox) imap.Mailbox { return &panicStore{sm} }, run: func(t *testing.T, h *imapHarness) {
			h.send("f UID FETCH 7 BODY[]")
			_ = h.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			_, _ = io.Copy(io.Discard, h.cr)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockBackend()
			mock.seed("INBOX", 7, len(raw), raw)
			dir := t.TempDir()
			sm := &fetchStreamMailbox{mockMailbox: mock.mbox, open: spoolTo(dir)}
			if tc.setup != nil {
				tc.setup(sm, dir)
			}
			var mbox imap.Mailbox = sm
			if tc.mbox != nil {
				mbox = tc.mbox(sm)
			}
			h := newIMAPHarnessSession(t, &streamBackend{mockBackend: mock, mbox: mbox}, mock.user, mock.pass, nil)
			h.login("a1")
			h.selectInbox("a2")
			tc.run(t, h)
			_ = h.conn.Close()
			<-h.done
			if sm.streams.Load() != 1 {
				t.Fatalf("FetchStream called %d times", sm.streams.Load())
			}
			if sm.opened.Load() != 0 {
				t.Errorf("%d streamed messages left open", sm.opened.Load())
			}
			if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
				t.Errorf("spool files left: %v", left)
			}
		})
	}
}

// midCopyFailure lets the scan read the whole message and fails the copy after
// its first piece.
type midCopyFailure struct {
	imap.FetchedMessage
	mu    sync.Mutex
	calls *int
}

func (m *midCopyFailure) ReadAt(p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if off == 0 {
		*m.calls++
	}
	if *m.calls >= 2 && off > 1<<20 {
		return 0, errors.New("disk gone")
	}
	return m.FetchedMessage.ReadAt(p, off)
}
