package server

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// These tests pin the per-connection panic-recovery contract (issue #10): a
// panic raised inside a backend method must be contained to the offending
// client's goroutine. On the unfixed engine the panic escapes the serve
// goroutine and aborts the whole test binary (which is exactly how it took the
// production server down); with recovery in place the failing connection is
// closed with a "* BYE" and every other client keeps being served.

// panicTestBackend authenticates the mock credentials and hands out a
// caller-supplied Mailbox, so a test can inject a mailbox whose methods panic.
type panicTestBackend struct{ mbox imap.Mailbox }

func (b panicTestBackend) Authenticate(user, pass string) (imap.Mailbox, error) {
	if user == "alice@example.com" && pass == "s3cret" {
		return b.mbox, nil
	}
	return nil, fmt.Errorf("invalid credentials")
}

// panicOnMessagesMailbox panics on every Messages call. Reached via SELECT, it
// makes a backend method blow up in the main serve goroutine.
type panicOnMessagesMailbox struct{ *mockMailbox }

func (p *panicOnMessagesMailbox) Messages(string) ([]imap.Message, error) {
	panic("test-induced panic in Mailbox.Messages")
}

// panicOnPollMailbox serves the first Messages call (the one SELECT makes) and
// panics on every later call — i.e. the ones the IDLE poll goroutine makes, so
// the panic originates in that separate goroutine.
type panicOnPollMailbox struct {
	*mockMailbox
	calls atomic.Int32
}

func (p *panicOnPollMailbox) Messages(folder string) ([]imap.Message, error) {
	if p.calls.Add(1) > 1 {
		panic("test-induced panic in IDLE poll Mailbox.Messages")
	}
	return p.mockMailbox.Messages(folder)
}

// startPanicServer runs a real Server accept loop on an ephemeral port, using
// the production per-connection spawn path, and returns its dial address.
func startPanicServer(t *testing.T, backend imap.Backend) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := New("imap.test", backend, nil, imap.NopLimiter{})
	go srv.acceptLoop(ln, false)
	t.Cleanup(func() {
		_ = srv.Close() // stops accepting and force-closes any live session
		_ = ln.Close()  // unblock the manually-started acceptLoop's Accept
	})
	return ln.Addr().String()
}

// rawClient is a minimal line-oriented IMAP client over a raw socket.
type rawClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dialIMAP(t *testing.T, addr string) *rawClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &rawClient{t: t, conn: conn, r: bufio.NewReader(conn)}
	if g, err := c.readLine(); err != nil || !strings.HasPrefix(g, "* OK") {
		t.Fatalf("greeting = %q, err = %v", g, err)
	}
	return c
}

func (c *rawClient) readLine() (string, error) {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *rawClient) send(line string) {
	c.t.Helper()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(c.conn, "%s\r\n", line); err != nil {
		c.t.Fatalf("send %q: %v", line, err)
	}
}

// mustOK sends a tagged command and asserts the tagged completion is OK.
func (c *rawClient) mustOK(line string) {
	c.t.Helper()
	tag := strings.SplitN(line, " ", 2)[0]
	c.send(line)
	for {
		l, err := c.readLine()
		if err != nil {
			c.t.Fatalf("command %q: connection closed before tagged reply: %v", line, err)
		}
		if strings.HasPrefix(l, tag+" ") {
			if !strings.Contains(l, " OK") {
				c.t.Fatalf("command %q status = %q, want OK", line, l)
			}
			return
		}
	}
}

// drainUntilClosed reads until the peer closes the connection, reporting whether
// a "* BYE" line was seen. A contained panic closes the connection promptly, so
// this returns quickly; if recovery were missing the process would have already
// aborted before reaching here.
func (c *rawClient) drainUntilClosed() (sawBye bool) {
	c.t.Helper()
	for {
		l, err := c.readLine()
		if err != nil {
			return sawBye
		}
		if strings.HasPrefix(l, "* BYE") {
			sawBye = true
		}
	}
}

// TestServer_RecoversHandlerPanic_KeepsServingOthers drives a backend panic in
// the main serve goroutine (via SELECT -> Mailbox.Messages) and asserts the
// server contains it: the failing client is closed with a BYE and a second
// client on the same server still completes a command.
func TestServer_RecoversHandlerPanic_KeepsServingOthers(t *testing.T) {
	m := newMockBackend()
	backend := panicTestBackend{mbox: &panicOnMessagesMailbox{mockMailbox: m.mbox}}
	addr := startPanicServer(t, backend)

	// Client 1: authenticate, then a command that panics the backend.
	c1 := dialIMAP(t, addr)
	c1.mustOK("a1 LOGIN alice@example.com s3cret")
	c1.send("a2 SELECT INBOX") // Mailbox.Messages panics here

	if !c1.drainUntilClosed() {
		t.Errorf("client1: expected a '* BYE' before the connection closed after the backend panic")
	}

	// Client 2: a fresh connection to the SAME server must still work.
	c2 := dialIMAP(t, addr)
	c2.mustOK("b1 LOGIN alice@example.com s3cret")
	c2.mustOK("b2 NOOP")
}

// TestServer_RecoversIdlePollPanic_KeepsServingOthers drives a backend panic in
// the IDLE poll goroutine (a *separate* goroutine that the Handle-level recover
// cannot catch) and asserts it too is contained: the idling client is closed
// and a second client on the same server still completes a command.
func TestServer_RecoversIdlePollPanic_KeepsServingOthers(t *testing.T) {
	old := idlePollInterval
	idlePollInterval = 10 * time.Millisecond
	t.Cleanup(func() { idlePollInterval = old })

	m := newMockBackend()
	m.seed("INBOX", 5, 100, "Subject: Msg 5\r\n\r\nbody 5\r\n")
	backend := panicTestBackend{mbox: &panicOnPollMailbox{mockMailbox: m.mbox}}
	addr := startPanicServer(t, backend)

	c1 := dialIMAP(t, addr)
	c1.mustOK("a1 LOGIN alice@example.com s3cret")
	c1.mustOK("a2 SELECT INBOX") // first Messages call succeeds
	c1.send("a3 IDLE")
	if l, err := c1.readLine(); err != nil || l != "+ idling" {
		t.Fatalf("IDLE continuation = %q, err = %v", l, err)
	}
	// The poll ticker fires (~10ms) and Mailbox.Messages panics in the poll
	// goroutine; recovery must close this connection rather than crash.
	if !c1.drainUntilClosed() {
		t.Errorf("client1: expected a '* BYE' before the connection closed after the IDLE poll panic")
	}

	c2 := dialIMAP(t, addr)
	c2.mustOK("b1 LOGIN alice@example.com s3cret")
	c2.mustOK("b2 NOOP")
}
