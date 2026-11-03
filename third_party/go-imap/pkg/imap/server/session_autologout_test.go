package server

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// These tests pin the autologout contract (issue #34): when the inactivity
// timer fires, the server MUST announce the close with an untagged "* BYE"
// (RFC 3501 §5.4 defines the timer, §7.1.5 defines BYE) before dropping the
// connection, rather than disconnecting silently. On the pre-fix engine the read
// deadline expiry returned straight to the deferred Close with no BYE, so the
// two "SendsBye" tests fail RED there.

// startIdleSession runs a real Session over net.Pipe with a shortened autologout
// timeout and returns the client end plus a buffered reader positioned just past
// the greeting. The caller drives the client; the session goroutine is joined on
// cleanup.
func startIdleSession(t *testing.T, backend imap.Backend, timeout time.Duration) (net.Conn, *bufio.Reader) {
	t.Helper()

	old := autologoutTimeout
	autologoutTimeout = timeout
	t.Cleanup(func() { autologoutTimeout = old })

	client, server := net.Pipe()
	sess := NewSession(server, backend, "imap.test", nil, imap.NopLimiter{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})

	cr := bufio.NewReader(client)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if g, err := cr.ReadString('\n'); err != nil || !strings.HasPrefix(g, "* OK") {
		t.Fatalf("greeting = %q, err = %v", g, err)
	}
	return client, cr
}

// drainForBye reads lines until the peer closes the connection, reporting whether
// an untagged "* BYE" naming the autologout arrived before EOF. It bounds each
// read so a silently-dropped connection (the RED case) fails fast rather than
// hanging until the test deadline.
func drainForBye(t *testing.T, conn net.Conn, cr *bufio.Reader) (sawBye bool) {
	t.Helper()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := cr.ReadString('\n')
		if err != nil {
			return sawBye // EOF (or the guard deadline) — connection closed.
		}
		l := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(l, "* BYE") && strings.Contains(strings.ToLower(l), "autologout") {
			sawBye = true
		}
	}
}

// TestSession_AutologoutSendsBye lets the inactivity timer fire while the client
// sits at the authenticated-command prompt and asserts a "* BYE" precedes EOF.
func TestSession_AutologoutSendsBye(t *testing.T) {
	conn, cr := startIdleSession(t, newMockBackend(), 50*time.Millisecond)

	// Stay silent: the autologout timer must fire and close the connection.
	if !drainForBye(t, conn, cr) {
		t.Errorf("expected an untagged '* BYE Autologout...' before EOF on inactivity autologout; connection was dropped silently")
	}
}

// TestSession_AutologoutDuringIdleSendsBye drives the client into an active IDLE
// command, then stays silent so the autologout fires while IDLE owns the reader.
// The BYE must still be delivered before the connection closes.
func TestSession_AutologoutDuringIdleSendsBye(t *testing.T) {
	conn, cr := startIdleSession(t, newMockBackend(), 80*time.Millisecond)

	// LOGIN, SELECT INBOX, then IDLE — each command resets the deadline, so the
	// setup completes well within the window; the timer then fires during IDLE.
	writeCmd(t, conn, "a1 LOGIN alice@example.com s3cret")
	readUntilTag(t, conn, cr, "a1")
	writeCmd(t, conn, "a2 SELECT INBOX")
	readUntilTag(t, conn, cr, "a2")
	writeCmd(t, conn, "a3 IDLE")
	if l := readOneLine(t, conn, cr); l != "+ idling" {
		t.Fatalf("IDLE continuation = %q, want %q", l, "+ idling")
	}

	if !drainForBye(t, conn, cr) {
		t.Errorf("expected an untagged '* BYE Autologout...' before EOF when the autologout fires during IDLE; connection was dropped silently")
	}
}

// TestSession_CommandActivityResetsAutologout confirms the fix does not turn the
// autologout into an absolute-lifetime cap: a command issued before the window
// elapses resets the timer, so a client that stays active is never logged out.
// Each gap here is well under the window, yet their sum exceeds it — which would
// trip a non-resetting timer.
func TestSession_CommandActivityResetsAutologout(t *testing.T) {
	conn, cr := startIdleSession(t, newMockBackend(), 300*time.Millisecond)

	for i := 0; i < 4; i++ {
		time.Sleep(50 * time.Millisecond) // < 300ms window: the prior command reset it
		tag := fmt.Sprintf("n%d", i)
		writeCmd(t, conn, tag+" NOOP")
		status := readUntilTag(t, conn, cr, tag)
		if !strings.Contains(status, " OK") {
			t.Fatalf("NOOP %d status = %q, want OK (session autologged out despite activity?)", i, status)
		}
	}
}

// ── small raw-client helpers (no t.Fatalf on EOF, unlike the shared harness) ──

func writeCmd(t *testing.T, conn net.Conn, line string) {
	t.Helper()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(conn, "%s\r\n", line); err != nil {
		t.Fatalf("write %q: %v", line, err)
	}
}

func readOneLine(t *testing.T, conn net.Conn, cr *bufio.Reader) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := cr.ReadString('\n')
	if err != nil {
		t.Fatalf("readLine: %v (partial %q)", err, line)
	}
	return strings.TrimRight(line, "\r\n")
}

// readUntilTag reads (skipping untagged lines) until the line prefixed with tag,
// returning that tagged status line.
func readUntilTag(t *testing.T, conn net.Conn, cr *bufio.Reader, tag string) string {
	t.Helper()
	for {
		l := readOneLine(t, conn, cr)
		if strings.HasPrefix(l, tag+" ") {
			return l
		}
	}
}

// TestAutologoutIsAtLeastThirtyMinutes: "If a server has an inactivity
// autologout timer, the duration of that timer MUST be at least 30 minutes"
// (RFC 3501 §5.4).
func TestAutologoutIsAtLeastThirtyMinutes(t *testing.T) {
	if autologoutTimeout < 30*time.Minute {
		t.Fatalf("autologout after %v, want at least 30 minutes", autologoutTimeout)
	}
}
