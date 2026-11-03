package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// freePort returns a currently-unused TCP port. There is an unavoidable gap
// between closing the probe listener and the server re-binding the port, but it
// is small enough to be reliable for tests.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// dialClient opens a client connection to the server on port and consumes the
// greeting, returning the connection and a buffered reader over it.
func dialClient(t *testing.T, port int) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "* OK") {
		t.Fatalf("greeting = %q err=%v, want * OK...", line, err)
	}
	return conn, r
}

// clientLogin drives LOGIN with the mock backend's valid credentials, leaving
// the server session parked in the authenticated state — i.e. in flight,
// blocked reading the next command.
func clientLogin(t *testing.T, conn net.Conn, r *bufio.Reader) {
	t.Helper()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprintf(conn, "a1 LOGIN alice@example.com s3cret\r\n"); err != nil {
		t.Fatalf("write LOGIN: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "a1 OK") {
		t.Fatalf("LOGIN -> %q err=%v, want a1 OK...", line, err)
	}
}

// slowBackend is the mock backend whose "Slow" folder takes until release is
// closed to list, so a session can be held in the middle of a command.
type slowBackend struct {
	*mockBackend
	entered chan struct{}
	release chan struct{}
}

func newSlowBackend() *slowBackend {
	return &slowBackend{mockBackend: newMockBackend(), entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (b *slowBackend) Authenticate(user, pass string) (imap.Mailbox, error) {
	m, err := b.mockBackend.Authenticate(user, pass)
	if err != nil {
		return nil, err
	}
	return slowMailbox{m.(*mockMailbox), b}, nil
}

type slowMailbox struct {
	*mockMailbox
	b *slowBackend
}

func (m slowMailbox) Messages(folder string) ([]imap.Message, error) {
	if folder == "Slow" {
		m.b.entered <- struct{}{}
		<-m.b.release
		return nil, errors.New("slow folder")
	}
	return m.mockMailbox.Messages(folder)
}

// selectSlow sends SELECT Slow and waits until the session is inside the backend
// call, in the middle of the command.
func selectSlow(t *testing.T, b *slowBackend, conn net.Conn) {
	t.Helper()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprintf(conn, "a2 SELECT Slow\r\n"); err != nil {
		t.Fatalf("write SELECT: %v", err)
	}
	select {
	case <-b.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("SELECT never reached the backend")
	}
}

// readLines reads lines until EOF or the deadline.
func readLines(conn net.Conn, r *bufio.Reader) []string {
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			lines = append(lines, strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			return lines
		}
	}
}

// TestShutdownWaitsForInflightSession is the regression test for issue #33:
// Shutdown must block until every in-flight session has finished, not return as
// soon as the accept loops stop. A session in the middle of a command finishes
// it, and only then says BYE and closes.
func TestShutdownWaitsForInflightSession(t *testing.T) {
	port := freePort(t)
	b := newSlowBackend()
	srv := New("imap.test", b, nil, nil)
	if err := srv.ListenAndServe(Ports{IMAP: port}); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}

	conn, r := dialClient(t, port)
	defer func() { _ = conn.Close() }()
	clientLogin(t, conn, r)
	selectSlow(t, b, conn)

	returned := make(chan error, 1)
	go func() { returned <- srv.Shutdown(context.Background()) }()

	// Shutdown must NOT return while a command is still running.
	select {
	case err := <-returned:
		t.Fatalf("Shutdown returned (err=%v) while a command was still running; it must drain first", err)
	case <-time.After(300 * time.Millisecond):
	}

	// The command finishes, is answered, and then the session says BYE.
	close(b.release)
	lines := readLines(conn, r)
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "a2 NO ") || lines[1] != "* BYE Server shutting down" {
		t.Fatalf("after the command: %q, want its tagged NO, then * BYE Server shutting down", lines)
	}
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Shutdown after drain: got err %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return after the in-flight session ended")
	}
}

// TestShutdownSaysByeToIdleSessions: "A server MUST NOT unilaterally close the
// connection without sending an untagged BYE response that contains the reason
// for having done so" (RFC 3501 §3.4). A session waiting for its next command,
// and one in IDLE, each get the BYE and close at once, so Shutdown does not wait
// for clients that would never finish.
func TestShutdownSaysByeToIdleSessions(t *testing.T) {
	port := freePort(t)
	srv := New("imap.test", newMockBackend(), nil, nil)
	if err := srv.ListenAndServe(Ports{IMAP: port}); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}

	waiting, wr := dialClient(t, port)
	defer func() { _ = waiting.Close() }()
	clientLogin(t, waiting, wr)

	idling, ir := dialClient(t, port)
	defer func() { _ = idling.Close() }()
	clientLogin(t, idling, ir)
	_ = idling.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprintf(idling, "a2 SELECT INBOX\r\na3 IDLE\r\n"); err != nil {
		t.Fatalf("write IDLE: %v", err)
	}
	_ = idling.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		line, err := ir.ReadString('\n')
		if err != nil {
			t.Fatalf("before IDLE's continuation: %v", err)
		}
		if strings.HasPrefix(line, "+ ") {
			break
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v, want the idle sessions to end at once", err)
	}
	for name, c := range map[string]struct {
		conn net.Conn
		r    *bufio.Reader
	}{"waiting": {waiting, wr}, "idling": {idling, ir}} {
		if lines := readLines(c.conn, c.r); len(lines) != 1 || lines[0] != "* BYE Server shutting down" {
			t.Errorf("%s session got %q, want * BYE Server shutting down and then the close", name, lines)
		}
	}
}

// TestShutdownRespectsContextTimeout verifies that when a session cannot finish
// its command, Shutdown gives up at the context deadline and reports the ctx
// error rather than blocking forever.
func TestShutdownRespectsContextTimeout(t *testing.T) {
	port := freePort(t)
	b := newSlowBackend()
	srv := New("imap.test", b, nil, nil)
	if err := srv.ListenAndServe(Ports{IMAP: port}); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	t.Cleanup(func() { close(b.release); _ = srv.Close() })

	conn, r := dialClient(t, port)
	defer func() { _ = conn.Close() }()
	clientLogin(t, conn, r)
	selectSlow(t, b, conn)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := srv.Shutdown(ctx)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Shutdown returned after only %v; expected it to block until ~the deadline", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Shutdown took %v; expected it to return near the deadline", elapsed)
	}
}

// TestCloseInterruptsInflightSession verifies that Close is an immediate hard
// stop: it force-closes live connections and returns without a graceful drain,
// leaving a subsequent Shutdown nothing to wait for.
func TestCloseInterruptsInflightSession(t *testing.T) {
	port := freePort(t)
	srv := New("imap.test", newMockBackend(), nil, nil)
	if err := srv.ListenAndServe(Ports{IMAP: port}); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}

	conn, r := dialClient(t, port)
	defer func() { _ = conn.Close() }()
	clientLogin(t, conn, r)

	done := make(chan error, 1)
	go func() { done <- srv.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return promptly")
	}

	// No sessions remain, so a graceful Shutdown returns at once.
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown after Close: %v", err)
	}
}
