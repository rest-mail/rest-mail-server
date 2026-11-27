package server

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// streamMailbox is a mockMailbox that takes APPEND from the spool file (imap.StreamAppender).
// It records what it read and how many files the spool directory held while it read, and
// fails or panics when told to.
type streamMailbox struct {
	*mockMailbox
	spoolDir string
	fail     bool
	panics   bool

	mu      sync.Mutex
	got     []byte
	date    time.Time
	spooled int
}

func (m *streamMailbox) AppendFrom(dest string, f imap.FlagUpdate, internalDate time.Time, msg io.ReaderAt, size int64) (uint32, error) {
	if m.panics {
		panic("append interrupted")
	}
	got, err := io.ReadAll(io.NewSectionReader(msg, 0, size))
	if err != nil {
		return 0, err
	}
	files, _ := os.ReadDir(m.spoolDir)
	m.mu.Lock()
	m.got, m.date, m.spooled = got, internalDate, len(files)
	m.mu.Unlock()
	if m.fail {
		return 0, errors.New("store refused")
	}
	return 0, m.Append(dest, f, got)
}

func newStreamHarness(t *testing.T) (*imapHarness, *streamMailbox, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	mock := newMockBackend()
	mbox := &streamMailbox{mockMailbox: mock.mbox, spoolDir: dir}
	h := newIMAPHarnessWith(t, panicTestBackend{mbox: mbox}, mock.user, mock.pass)
	h.login("a1")
	return h, mbox, dir
}

// syncSession waits for the session to finish the command before, whose tagged answer is
// sent before its spool file is removed.
func syncSession(h *imapHarness) {
	h.t.Helper()
	if _, st := h.command("s1", "NOOP"); !strings.HasPrefix(st, "s1 OK") {
		h.t.Fatalf("NOOP = %q", st)
	}
}

func spoolFiles(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// A StreamAppender reads the literal's exact octets from a file, with or without the
// client's date-time, and the file is gone once the APPEND is answered.
func TestAppendStreamsTheLiteralFromASpoolFile(t *testing.T) {
	for _, c := range []struct {
		name, args string
		date       time.Time
	}{
		{name: "no date-time", args: "INBOX"},
		{name: "date-time", args: `INBOX (\Seen) "07-Feb-1994 22:43:04 -0800"`,
			date: time.Date(1994, time.February, 7, 22, 43, 4, 0, time.FixedZone("", -8*3600))},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, mbox, dir := newStreamHarness(t)
			body := "Subject: s\n\r\n8-bit \xe9\xff, bare CR \r, and LF\n" + strings.Repeat("x", 3*appendSpoolChunk+7)
			h.send("a2 APPEND %s {%d}", c.args, len(body))
			if got := h.readLine(); !strings.HasPrefix(got, "+") {
				t.Fatalf("continuation = %q", got)
			}
			h.send("%s", body)
			if st := h.readLine(); !strings.HasPrefix(st, "a2 OK") {
				t.Fatalf("APPEND = %q", st)
			}
			syncSession(h)
			mbox.mu.Lock()
			defer mbox.mu.Unlock()
			if string(mbox.got) != body {
				t.Fatalf("AppendFrom read %d octets, want the literal's %d", len(mbox.got), len(body))
			}
			if !mbox.date.Equal(c.date) {
				t.Errorf("AppendFrom date = %v, want %v", mbox.date, c.date)
			}
			if mbox.spooled != 1 {
				t.Errorf("spool directory held %d files during AppendFrom, want the one spool file", mbox.spooled)
			}
			if files := spoolFiles(t, dir); len(files) != 0 {
				t.Errorf("spool left %v", files)
			}
		})
	}
}

// No APPEND leaves its spool file: one refused by the store, one whose store panics, one the
// client abandons mid-literal, and one refused for its octets.
func TestAppendSpoolIsRemovedHoweverTheCommandEnds(t *testing.T) {
	body := "Subject: s\r\n\r\nbody\r\n"
	t.Run("store refuses", func(t *testing.T) {
		h, mbox, dir := newStreamHarness(t)
		mbox.fail = true
		if _, st := h.appendMsg("a2", "INBOX", "", body); !strings.HasPrefix(st, "a2 NO") {
			t.Fatalf("APPEND = %q, want NO", st)
		}
		syncSession(h)
		if files := spoolFiles(t, dir); len(files) != 0 {
			t.Errorf("spool left %v", files)
		}
	})
	t.Run("store panics", func(t *testing.T) {
		h, mbox, dir := newStreamHarness(t)
		mbox.panics = true
		h.send("a2 APPEND INBOX {%d}", len(body))
		_ = h.readLine()
		h.send("%s", body)
		if line := h.readLine(); !strings.HasPrefix(line, "* BYE") {
			t.Fatalf("after a panic = %q, want * BYE", line)
		}
		<-h.done
		if files := spoolFiles(t, dir); len(files) != 0 {
			t.Errorf("spool left %v", files)
		}
	})
	t.Run("client leaves mid-literal", func(t *testing.T) {
		h, _, dir := newStreamHarness(t)
		h.send("a2 APPEND INBOX {%d}", 1<<20)
		_ = h.readLine()
		_, _ = fmt.Fprint(h.cw, strings.Repeat("y", 100<<10))
		_ = h.cw.Flush()
		_ = h.conn.Close()
		<-h.done
		if files := spoolFiles(t, dir); len(files) != 0 {
			t.Errorf("spool left %v", files)
		}
	})
	t.Run("NUL refused", func(t *testing.T) {
		h, mbox, dir := newStreamHarness(t)
		if _, st := h.appendMsg("a2", "INBOX", "", "Subject: s\r\n\r\na\x00b\r\n"); st != "a2 BAD NUL in message literal" {
			t.Fatalf("APPEND = %q", st)
		}
		if _, st := h.command("a3", "NOOP"); !strings.HasPrefix(st, "a3 OK") {
			t.Fatalf("NOOP after the refusal = %q", st)
		}
		if mbox.got != nil {
			t.Error("a refused literal reached the store")
		}
		if files := spoolFiles(t, dir); len(files) != 0 {
			t.Errorf("spool left %v", files)
		}
	})
}

// A store without AppendFrom still gets the octets whole, and the spool file is gone.
func TestAppendWithoutStreamAppenderReadsTheSpoolBack(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")
	body := "Subject: s\r\n\r\n" + strings.Repeat("\xe9z\n", appendSpoolChunk)
	if _, st := h.appendMsg("a2", "INBOX", "", body); !strings.HasPrefix(st, "a2 OK") {
		t.Fatalf("APPEND = %q", st)
	}
	op, ok := b.mbox.lastAppend()
	if !ok || string(op.raw) != body {
		t.Fatalf("Append got %d octets, want the literal's %d", len(op.raw), len(body))
	}
	syncSession(h)
	if files := spoolFiles(t, dir); len(files) != 0 {
		t.Errorf("spool left %v", files)
	}
}

// When the spool file cannot be made, the literal is still read to its end, the APPEND is
// answered NO and the session goes on.
func TestAppendSpoolFailureIsANo(t *testing.T) {
	t.Setenv("TMPDIR", "/nonexistent/imap-append-spool")
	b := newMockBackend()
	h := newIMAPHarness(t, b)
	h.login("a1")
	if _, st := h.appendMsg("a2", "INBOX", "", "Subject: s\r\n\r\nbody\r\n"); st != "a2 NO APPEND failed" {
		t.Fatalf("APPEND = %q, want a2 NO APPEND failed", st)
	}
	if _, st := h.command("a3", "NOOP"); !strings.HasPrefix(st, "a3 OK") {
		t.Fatalf("NOOP after the refusal = %q", st)
	}
	if _, ok := b.mbox.lastAppend(); ok {
		t.Error("an unspooled literal reached the store")
	}
}
