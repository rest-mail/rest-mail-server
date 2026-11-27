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

// dialPOP3 opens a client connection to the server on port and consumes the
// greeting, returning the connection and a buffered reader over it.
func dialPOP3(t *testing.T, port int) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "+OK") {
		t.Fatalf("greeting = %q err=%v, want +OK...", line, err)
	}
	return conn, r
}

// clientLogin drives USER/PASS with the mock backend's valid credentials,
// leaving the server session parked in the TRANSACTION state — i.e. in flight,
// blocked reading the next command.
func clientLogin(t *testing.T, conn net.Conn, r *bufio.Reader) {
	t.Helper()
	for _, cmd := range []string{"USER alice@example.com", "PASS s3cret"} {
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := fmt.Fprintf(conn, "%s\r\n", cmd); err != nil {
			t.Fatalf("write %q: %v", cmd, err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := r.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "+OK") {
			t.Fatalf("%s -> %q err=%v, want +OK...", cmd, line, err)
		}
	}
}

// TestShutdownWaitsForInflightSession is the regression test for issue #12:
// Shutdown must block until every in-flight session has finished, not return as
// soon as the accept loops stop. It first asserts Shutdown stays blocked while a
// session is live, then that it returns promptly once the session ends.
func TestShutdownWaitsForInflightSession(t *testing.T) {
	port := freePort(t)
	srv := New(newMockBackend(), nil, nil)
	if err := srv.ListenAndServe(Ports{POP3: port}); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}

	conn, r := dialPOP3(t, port)
	defer func() { _ = conn.Close() }()
	clientLogin(t, conn, r)
	// The session goroutine is now parked reading the next command: in flight.

	returned := make(chan error, 1)
	go func() { returned <- srv.Shutdown(context.Background()) }()

	// Shutdown must NOT return while a session is still being served.
	select {
	case err := <-returned:
		t.Fatalf("Shutdown returned (err=%v) while a session was still in flight; it must drain first", err)
	case <-time.After(300 * time.Millisecond):
	}

	// End the session; Shutdown must then return promptly with no error.
	_ = conn.Close()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Shutdown after drain: got err %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return after the in-flight session ended")
	}
}

// TestShutdownRespectsContextTimeout verifies that when a session refuses to
// finish, Shutdown gives up at the context deadline and reports the ctx error
// rather than blocking forever.
func TestShutdownRespectsContextTimeout(t *testing.T) {
	port := freePort(t)
	srv := New(newMockBackend(), nil, nil)
	if err := srv.ListenAndServe(Ports{POP3: port}); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	conn, r := dialPOP3(t, port)
	defer func() { _ = conn.Close() }()
	clientLogin(t, conn, r)
	// Session stays in flight: the client never sends QUIT or closes.

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
	srv := New(newMockBackend(), nil, nil)
	if err := srv.ListenAndServe(Ports{POP3: port}); err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}

	conn, r := dialPOP3(t, port)
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
