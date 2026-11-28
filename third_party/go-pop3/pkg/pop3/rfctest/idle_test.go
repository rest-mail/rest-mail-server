package rfctest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// RFC 1939 §3 permits an optional autologout timer of at least ten minutes.
const minIdle = 10 * time.Minute

// deadlineConn records the session's SetDeadline calls on the server end of
// net.Pipe. Each deadline is paired with the monotonic completion time of the
// server's latest Write, so the ten-minute bound needs no clock tolerance: the
// session can only compute its deadline after that write returned.
type deadlineConn struct {
	net.Conn
	mu        sync.Mutex
	lastWrite time.Time
	sets      []deadlineSet
	readErr   error
	reads     chan struct{} // one signal per Read entry, after any deadline
}

type deadlineSet struct{ deadline, afterWrite time.Time }

func (d deadlineSet) String() string {
	return fmt.Sprintf("deadline %v after write %v", d.deadline, d.afterWrite)
}

func (c *deadlineConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.mu.Lock()
	c.lastWrite = time.Now()
	c.mu.Unlock()
	return n, err
}
func (c *deadlineConn) SetDeadline(d time.Time) error {
	err := c.Conn.SetDeadline(d)
	c.mu.Lock()
	c.sets = append(c.sets, deadlineSet{deadline: d, afterWrite: c.lastWrite})
	c.mu.Unlock()
	return err
}
func (c *deadlineConn) Read(p []byte) (int, error) {
	c.reads <- struct{}{}
	n, err := c.Conn.Read(p)
	if err != nil {
		c.mu.Lock()
		c.readErr = err
		c.mu.Unlock()
	}
	return n, err
}
func (c *deadlineConn) snapshot() []deadlineSet {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]deadlineSet(nil), c.sets...)
}

type idleWire struct {
	*wire
	server *deadlineConn
	pipe   net.Conn // underlying server end, for forcing a real expiry
}

func idleConnect(t *testing.T, b pop3.Backend) *idleWire {
	t.Helper()
	client, server := net.Pipe()
	rec := &deadlineConn{Conn: server, reads: make(chan struct{}, 64)}
	done := make(chan struct{})
	go func() { defer close(done); pop3server.NewSession(rec, b, nil, nil).Handle() }()
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("session did not stop")
		}
	})
	w := &idleWire{wire: &wire{t: t, conn: client, reader: bufio.NewReader(client), done: done}, server: rec, pipe: server}
	w.status(w.line(), "+OK")
	return w
}

// awaitRead returns once the session has entered its next command Read, which
// it reaches only after renewing the deadline for that idle interval.
func (w *idleWire) awaitRead() {
	w.t.Helper()
	select {
	case <-w.server.reads:
	case <-time.After(5 * time.Second):
		w.t.Fatal("session did not wait for another command")
	}
}

// exchange sends one command, reads its single-line status and returns the
// deadline governing the following idle interval, with the client send time.
func (w *idleWire) exchange(command, want string) (deadlineSet, time.Time) {
	w.t.Helper()
	before := len(w.server.snapshot())
	sent := time.Now()
	w.status(w.cmd(command), want)
	w.awaitRead()
	sets := w.server.snapshot()
	if len(sets) != before+1 {
		w.t.Fatalf("%s: %d deadline renewals before the next read, want 1", command, len(sets)-before)
	}
	return sets[len(sets)-1], sent
}

// checklist: POP3-IDLE-01 POP3-IDLE-02
func TestIdleDeadlineReset(t *testing.T) {
	w := idleConnect(t, &updateMailbox{
		transactionMailbox: transactionMailbox{raw: transactionMessages},
		attempts:           map[string]int{}, removed: map[string]bool{},
	})
	w.awaitRead() // The greeting's idle interval.
	greeting := w.server.snapshot()
	if len(greeting) != 1 || greeting[0].deadline.Before(greeting[0].afterWrite.Add(minIdle)) {
		t.Fatalf("greeting deadlines = %v, want one at least %v after the greeting write", greeting, minIdle)
	}
	steps := []struct{ command, want string }{
		// AUTHORIZATION: unknown, refused and malformed commands, then login.
		{"BOGUS", "-ERR"}, {"STAT", "-ERR"}, {"USER", "-ERR"},
		{"USER alice", "+OK"}, {"PASS secret", "+OK"},
		// TRANSACTION: valid and invalid commands alternate.
		{"NOOP", "+OK"}, {"RETR 9", "-ERR"}, {"DELE 1", "+OK"},
		{"STAT extra", "-ERR"}, {"DELE 1", "-ERR"}, {"RSET", "+OK"},
	}
	previous := greeting[0].deadline
	for _, step := range steps {
		set, sent := w.exchange(step.command, step.want)
		// At least ten minutes after the reply's final write: the timer covers
		// the whole idle interval that this command's reply began.
		if set.afterWrite.IsZero() || set.deadline.Before(set.afterWrite.Add(minIdle)) {
			t.Fatalf("%s: deadline %v is under %v after the reply write %v", step.command, set.deadline, minIdle, set.afterWrite)
		}
		// Renewed from this command, not carried over from an earlier interval.
		if set.deadline.Before(sent.Add(minIdle)) || !set.deadline.After(previous) {
			t.Fatalf("%s: deadline %v was not reset after the command sent at %v (previous %v)", step.command, set.deadline, sent, previous)
		}
		previous = set.deadline
	}
	w.status(w.cmd("QUIT"), "+OK")
}

// checklist: POP3-IDLE-03
func TestIdleAutologoutWithoutUpdate(t *testing.T) {
	m := &updateMailbox{
		transactionMailbox: transactionMailbox{raw: transactionMessages},
		attempts:           map[string]int{}, removed: map[string]bool{},
	}
	want, err := m.Messages()
	if err != nil {
		t.Fatal(err)
	}
	w := idleConnect(t, m)
	w.awaitRead()
	w.exchange("USER alice", "+OK")
	w.exchange("PASS secret", "+OK")
	w.exchange("DELE 1", "+OK")
	set, _ := w.exchange("DELE 3", "+OK")
	if set.deadline.Before(set.afterWrite.Add(minIdle)) {
		t.Fatalf("deadline %v is under %v after the reply write %v", set.deadline, minIdle, set.afterWrite)
	}
	// The session is blocked in Read under its renewed deadline. Moving the
	// underlying read deadline into the past stands in for ten idle minutes
	// and yields a genuine net.Pipe timeout, not an EOF or a closed pipe.
	if err := w.pipe.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	// No response is sent: the client sees only the connection closing.
	_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if b, err := w.reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("after autologout: byte %q, err %v, want EOF without a response", b, err)
	}
	waitStopped(t, w.wire)
	w.server.mu.Lock()
	readErr := w.server.readErr
	w.server.mu.Unlock()
	var netErr net.Error
	if !errors.Is(readErr, os.ErrDeadlineExceeded) || !errors.As(readErr, &netErr) || !netErr.Timeout() {
		t.Fatalf("session read ended with %v, want a deadline timeout", readErr)
	}
	// UPDATE was never entered: no Delete, despite two marked messages.
	deletions(t, m, nil, nil)
	got, err := m.Messages()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("messages after autologout = %v (%v), want %v", got, err, want)
	}
}

// checklist: POP3-IDLE-04
func TestIdleTimeoutWithBufferedPartialCommand(t *testing.T) {
	for _, tc := range []struct{ name, partial string }{
		{"quit-without-terminator", "QUIT"},
		{"quit-with-cr-without-lf", "QUIT\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &updateMailbox{
				transactionMailbox: transactionMailbox{raw: transactionMessages},
				attempts:           map[string]int{}, removed: map[string]bool{},
			}
			want, err := m.Messages()
			if err != nil {
				t.Fatal(err)
			}
			w := idleConnect(t, m)
			w.awaitRead()
			w.exchange("USER alice", "+OK")
			w.exchange("PASS secret", "+OK")
			w.exchange("DELE 1", "+OK")
			w.exchange("DELE 3", "+OK")
			// The session is now blocked in Read, and its signal was consumed.
			// net.Pipe writes complete only when the server's Read has taken the
			// bytes, so the partial command is buffered once Write returns.
			_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if n, err := io.WriteString(w.conn, tc.partial); err != nil || n != len(tc.partial) {
				t.Fatalf("send partial command: %d bytes, %v", n, err)
			}
			// The next Read entry proves the incomplete line was buffered and
			// the session is again waiting for its terminating LF.
			w.awaitRead()
			if err := w.pipe.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if b, err := w.reader.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("after timeout: byte %q, err %v, want EOF without a response", b, err)
			}
			waitStopped(t, w.wire)
			w.server.mu.Lock()
			readErr := w.server.readErr
			w.server.mu.Unlock()
			var netErr net.Error
			if !errors.Is(readErr, os.ErrDeadlineExceeded) || !errors.As(readErr, &netErr) || !netErr.Timeout() {
				t.Fatalf("session read ended with %v, want a deadline timeout", readErr)
			}
			// The partial QUIT was never dispatched: no UPDATE, no Delete.
			deletions(t, m, nil, nil)
			got, err := m.Messages()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("messages after timeout = %v (%v), want %v", got, err, want)
			}
		})
	}
}

// authCountingBackend records every backend entry point. Authenticate is the
// only way into a mailbox, so a zero count also means no mailbox operation.
type authCountingBackend struct{ authenticated atomic.Int32 }

func (b *authCountingBackend) Authenticate(string, string) (pop3.Mailbox, error) {
	b.authenticated.Add(1)
	return nil, errors.New("unexpected authentication")
}

// checklist: POP3-IDLE-05
func TestIdlePreLoginTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		sent []string // complete commands answered before the timeout
	}{
		{"after-greeting", nil},
		{"after-user-before-pass", []string{"USER alice"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &authCountingBackend{}
			w := idleConnect(t, b)
			w.awaitRead() // The greeting's idle interval.
			for _, command := range tc.sent {
				w.exchange(command, "+OK")
			}
			// The session is blocked in Read in AUTHORIZATION and no PASS was
			// sent. Moving the underlying read deadline into the past stands
			// in for ten idle minutes with a genuine net.Pipe timeout.
			if err := w.pipe.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			// No response is sent: the client sees only the connection closing.
			_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if b, err := w.reader.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("after timeout: byte %q, err %v, want EOF without a response", b, err)
			}
			waitStopped(t, w.wire)
			w.server.mu.Lock()
			readErr := w.server.readErr
			w.server.mu.Unlock()
			var netErr net.Error
			if !errors.Is(readErr, os.ErrDeadlineExceeded) || !errors.As(readErr, &netErr) || !netErr.Timeout() {
				t.Fatalf("session read ended with %v, want a deadline timeout", readErr)
			}
			// Authenticate is the only route to a mailbox: never called.
			if n := b.authenticated.Load(); n != 0 {
				t.Fatalf("backend Authenticate called %d times, want 0", n)
			}
		})
	}
}

// checklist: POP3-IDLE-06
func TestIdlePreLoginPartialCommandTimeout(t *testing.T) {
	for _, tc := range []struct {
		name, complete, partial string
	}{
		{"user-without-terminator", "", "USER alice"},
		{"user-with-cr-without-lf", "", "USER alice\r"},
		{"pass-without-terminator", "USER alice", "PASS secret"},
		{"pass-with-cr-without-lf", "USER alice", "PASS secret\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &authCountingBackend{}
			w := idleConnect(t, b)
			w.awaitRead()
			if tc.complete != "" {
				w.exchange(tc.complete, "+OK")
			}
			// net.Pipe Write completes only after Read takes the bytes. The next
			// Read entry then proves ReadSlice buffered the incomplete command
			// and is waiting for its LF, rather than dispatching credentials.
			_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if n, err := io.WriteString(w.conn, tc.partial); err != nil || n != len(tc.partial) {
				t.Fatalf("send partial command: %d bytes, %v", n, err)
			}
			w.awaitRead()
			if n := b.authenticated.Load(); n != 0 {
				t.Fatalf("Authenticate called before timeout: %d", n)
			}
			// Force a genuine read timeout after buffering, without waiting ten
			// minutes or closing the pipe to manufacture EOF.
			if err := w.pipe.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if b, err := w.reader.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("after timeout: byte %q, err %v, want EOF without a response", b, err)
			}
			waitStopped(t, w.wire)
			w.server.mu.Lock()
			readErr := w.server.readErr
			w.server.mu.Unlock()
			var netErr net.Error
			if !errors.Is(readErr, os.ErrDeadlineExceeded) || !errors.As(readErr, &netErr) || !netErr.Timeout() {
				t.Fatalf("session read ended with %v, want a deadline timeout", readErr)
			}
			// Authenticate is the only mailbox entry point. Even the buffered
			// PASS after an accepted USER must never reach it on read failure.
			if n := b.authenticated.Load(); n != 0 {
				t.Fatalf("Authenticate called after timeout: %d", n)
			}
		})
	}
}
