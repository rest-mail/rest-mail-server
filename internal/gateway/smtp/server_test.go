package smtp

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/connlimiter"
)

// TestLimitListener_PerIPLimitAndRelease proves the connection limiter still
// acts at accept level under go-smtp: an over-limit connection is closed
// before any SMTP handling (no greeting), and closing an admitted connection
// releases its slot.
func TestLimitListener_PerIPLimitAndRelease(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 1, MaxGlobal: 10})
	srv := NewServer("smtp.test", back, nil, store, limiter).newSMTPServer(false)

	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(&limitListener{Listener: base, limiter: limiter}) }()
	t.Cleanup(func() { _ = srv.Close() })

	readGreeting := func(c net.Conn) (string, error) {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		return bufio.NewReader(c).ReadString('\n')
	}

	// First connection is admitted and greeted.
	c1, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if g, err := readGreeting(c1); err != nil || !strings.HasPrefix(g, "220") {
		t.Fatalf("first conn greeting = %q, %v", g, err)
	}

	// A second concurrent connection from the same IP exceeds MaxPerIP: the
	// limiter closes it before any SMTP handling, so no greeting arrives.
	c2, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if g, err := readGreeting(c2); err == nil {
		t.Fatalf("over-limit conn should be closed without a greeting, got %q", g)
	}
	_ = c2.Close()

	// Closing the first connection releases its slot; a fresh connection is
	// admitted again (release happens when the server notices the close, so
	// poll briefly).
	_ = c1.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c3, err := net.Dial("tcp", base.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		g, err := readGreeting(c3)
		_ = c3.Close()
		if err == nil && strings.HasPrefix(g, "220") {
			return // slot was released
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot never released; last greeting %q, err %v", g, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stubAddr is a fixed net.Addr for injected test connections.
type stubAddr string

func (a stubAddr) Network() string { return "tcp" }
func (a stubAddr) String() string  { return string(a) }

// slowAddrConn simulates a connection behind PROXY protocol whose peer-IP
// resolution blocks: go-proxyproto's RemoteAddr() does not return until the
// PROXY header has been read (up to a 10s timeout). RemoteAddr() here blocks
// until release is closed, and closes entered (once) the first time it is
// called so a test can detect that admission has started and is stuck.
type slowAddrConn struct {
	net.Conn
	addr        net.Addr
	release     <-chan struct{}
	enteredOnce sync.Once
	entered     chan struct{}
}

func (c *slowAddrConn) RemoteAddr() net.Addr {
	c.enteredOnce.Do(func() { close(c.entered) })
	<-c.release
	return c.addr
}

// queueListener hands out preloaded connections; Accept blocks for the next one
// or until Close. Its buffer is large enough that offering a connection never
// blocks, so a stalled accept loop surfaces as a connection that is simply
// never served rather than as a test hang.
type queueListener struct {
	conns    chan net.Conn
	closed   chan struct{}
	closeOne sync.Once
}

func newQueueListener(buffer int) *queueListener {
	return &queueListener{conns: make(chan net.Conn, buffer), closed: make(chan struct{})}
}

func (l *queueListener) offer(c net.Conn) { l.conns <- c }

func (l *queueListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *queueListener) Close() error {
	l.closeOne.Do(func() { close(l.closed) })
	return nil
}

func (l *queueListener) Addr() net.Addr { return stubAddr("queue") }

// TestLimitListener_SlowProxyHeaderDoesNotStallAccept proves the accept loop is
// not serialized on per-connection peer-IP resolution. A first connection whose
// RemoteAddr() blocks — as it does behind a slowly-delivered PROXY header — must
// not delay accepting and greeting a second connection. Before the fix,
// admission resolved the IP inside limitListener.Accept, so one slow client
// stalled the entire accept loop (a DoS on new connections).
func TestLimitListener_SlowProxyHeaderDoesNotStallAccept(t *testing.T) {
	back := newMockBackend()
	store := newMockStore()
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 1000})
	srv := NewServer("smtp.test", back, nil, store, limiter).newSMTPServer(false)

	ql := newQueueListener(4)
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(&limitListener{Listener: ql, limiter: limiter})
	}()
	// Registered first => runs last: stop Serve and wait for it to return.
	t.Cleanup(func() {
		_ = srv.Close()
		<-served
	})

	// Connection 1: peer-IP resolution blocks until released, simulating a
	// client dribbling its PROXY header.
	slowClient, slowServer := net.Pipe()
	release1 := make(chan struct{})
	slow := &slowAddrConn{
		Conn:    slowServer,
		addr:    stubAddr("10.0.0.1:1111"),
		release: release1,
		entered: make(chan struct{}),
	}
	// Registered after the Serve cleanup => runs before it: unblock the stuck
	// admission so its goroutine can exit, then close the client end.
	t.Cleanup(func() {
		close(release1)
		_ = slowClient.Close()
	})
	ql.offer(slow)

	// Wait until connection 1 is actually blocked in RemoteAddr(): its admission
	// goroutine is now stuck. If the accept loop were serialized on it, nothing
	// below could be accepted.
	select {
	case <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("connection 1 never reached peer-IP resolution")
	}

	// Connection 2: peer-IP resolution returns immediately (pre-released).
	fastClient, fastServer := net.Pipe()
	release2 := make(chan struct{})
	close(release2)
	fast := &slowAddrConn{
		Conn:    fastServer,
		addr:    stubAddr("10.0.0.2:2222"),
		release: release2,
		entered: make(chan struct{}),
	}
	t.Cleanup(func() { _ = fastClient.Close() })
	ql.offer(fast)

	// The second connection must be greeted promptly despite connection 1 being
	// stuck in admission.
	_ = fastClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	greeting, err := bufio.NewReader(fastClient).ReadString('\n')
	if err != nil || !strings.HasPrefix(greeting, "220") {
		t.Fatalf("second connection greeting = %q, err = %v; want 220... (the slow connection stalled the accept loop)", greeting, err)
	}
}
