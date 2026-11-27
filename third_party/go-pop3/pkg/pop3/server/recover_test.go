package server

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-pop3/pkg/pop3"
)

// panicMailbox is a Mailbox whose Messages method panics, simulating a buggy
// backend that blows up mid-session. Without per-connection panic recovery this
// takes down the whole process and every concurrent session (issue #6).
type panicMailbox struct{}

func (panicMailbox) Messages() ([]pop3.Message, error) { panic("backend exploded") }
func (panicMailbox) Retrieve(string) ([]byte, error)   { return nil, nil }
func (panicMailbox) MarkSeen(string) error             { return nil }
func (panicMailbox) Delete(string) error               { return nil }

// panicBackend authenticates any credentials and hands out a panicMailbox.
type panicBackend struct{}

func (panicBackend) Authenticate(string, string) (pop3.Mailbox, error) { return panicMailbox{}, nil }

// nilBackend authenticates successfully but returns a nil Mailbox — the buggy
// (nil, nil) return described in issue #6. Dereferencing it nil-panics.
type nilBackend struct{}

func (nilBackend) Authenticate(string, string) (pop3.Mailbox, error) { return nil, nil }

// rawHarness drives a real Session over net.Pipe against an arbitrary Backend,
// so tests can script client commands and read the exact server replies.
type rawHarness struct {
	t    *testing.T
	conn net.Conn
	cr   *bufio.Reader
	cw   *bufio.Writer
	done chan struct{}
}

func newRawHarness(t *testing.T, backend pop3.Backend) *rawHarness {
	t.Helper()
	client, server := net.Pipe()
	sess := NewSession(server, backend, nil, pop3.NopLimiter{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()

	h := &rawHarness{
		t:    t,
		conn: client,
		cr:   bufio.NewReader(client),
		cw:   bufio.NewWriter(client),
		done: done,
	}
	t.Cleanup(func() {
		_ = client.Close()
		<-h.done
	})

	if g := h.readLine(); !strings.HasPrefix(g, "+OK") {
		t.Fatalf("greeting = %q, want +OK...", g)
	}
	return h
}

func (h *rawHarness) readLine() string {
	h.t.Helper()
	_ = h.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := h.cr.ReadString('\n')
	if err != nil {
		h.t.Fatalf("readLine: %v (partial %q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

func (h *rawHarness) cmd(line string) string {
	h.t.Helper()
	_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = h.cw.WriteString(line + "\r\n")
	if err := h.cw.Flush(); err != nil {
		h.t.Fatalf("send %q: %v", line, err)
	}
	return h.readLine()
}

// TestPOP3_SessionPanicRecovered proves a panic in a single client session is
// isolated: the server replies -ERR and returns instead of letting the panic
// unwind the goroutine and abort the whole process. Without recovery the panic
// is unhandled and crashes the test binary.
func TestPOP3_SessionPanicRecovered(t *testing.T) {
	h := newRawHarness(t, panicBackend{})

	if got := h.cmd("USER alice"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("USER = %q, want +OK", got)
	}
	// PASS triggers mailbox.Messages(), which panics. Recovery must turn that
	// into a clean -ERR rather than a process-killing crash.
	if got := h.cmd("PASS secret"); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("PASS against panicking backend = %q, want -ERR", got)
	}
	// The session goroutine must have returned (recovered), not crashed.
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not return after a panic; recovery missing")
	}
}

// TestPOP3_NilMailboxRejected proves a backend that returns (nil, nil) from
// Authenticate is rejected with -ERR and the session stays in AUTHORIZATION,
// rather than nil-dereferencing mailbox.Messages() and panicking.
func TestPOP3_NilMailboxRejected(t *testing.T) {
	h := newRawHarness(t, nilBackend{})

	if got := h.cmd("USER alice"); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("USER = %q, want +OK", got)
	}
	if got := h.cmd("PASS secret"); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("PASS with nil mailbox = %q, want -ERR", got)
	}
	// Session must remain alive and unauthenticated (still in AUTHORIZATION):
	// a command requiring auth is refused, proving no crash and no false login.
	if got := h.cmd("STAT"); !strings.HasPrefix(got, "-ERR") {
		t.Errorf("STAT after nil-mailbox login = %q, want -ERR (not authenticated)", got)
	}
}
