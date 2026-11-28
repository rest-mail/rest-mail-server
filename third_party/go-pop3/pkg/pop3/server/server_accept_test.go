package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/go-pop3/pkg/pop3"
)

// controlledListener injects Accept results without sockets or timing races.
// A successful Accept may already have acquired a connection when Close runs.
type controlledListener struct {
	accept func() (net.Conn, error)
	closed chan struct{}
	once   sync.Once
}

func (l *controlledListener) Accept() (net.Conn, error) { return l.accept() }
func (l *controlledListener) Addr() net.Addr            { return &net.TCPAddr{} }
func (l *controlledListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

type acceptTestConn struct{ net.Conn }

func (acceptTestConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 12345}
}

type acceptTestLimiter struct {
	pop3.NopLimiter
	mu       sync.Mutex
	accepted []string
	released []string
}

func (l *acceptTestLimiter) Accept(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.accepted = append(l.accepted, ip)
	return true
}

func (l *acceptTestLimiter) Release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = append(l.released, ip)
}

func (l *acceptTestLimiter) checkBalanced(t *testing.T) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	want := []string{"192.0.2.1"}
	if !reflect.DeepEqual(l.accepted, want) || !reflect.DeepEqual(l.released, want) {
		t.Errorf("limiter Accept = %v, Release = %v; want exactly %v each", l.accepted, l.released, want)
	}
}

// Register the fake listener just as listen does, including the accept-loop
// wait-group slot. Cleanup is bounded even if a regression strands a session.
func startControlledAccept(t *testing.T, srv *Server, ln *controlledListener) <-chan struct{} {
	t.Helper()
	srv.listeners = append(srv.listeners, ln)
	srv.wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer srv.wg.Done()
		srv.acceptLoop(ln, false)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("drain controlled server: %v", err)
		}
	})
	return done
}

// An Accept error while running must not stop service. The next accepted client
// receives a greeting, logs in, and quits normally; the failed Accept consumes
// no limiter slot, and shutdown still drains the loop and session.
func TestAcceptErrorRecovers(t *testing.T) {
	client, conn := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() { _ = conn.Close() })
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	limiter := &acceptTestLimiter{}
	srv := New(newMockBackend(), nil, limiter)
	ln := &controlledListener{closed: make(chan struct{})}
	calls := 0 // only the accept-loop goroutine writes; read after it exits
	ln.accept = func() (net.Conn, error) {
		calls++
		switch calls {
		case 1:
			return nil, errors.New("injected Accept error")
		case 2:
			return acceptTestConn{conn}, nil
		default:
			<-ln.closed
			return nil, net.ErrClosed
		}
	}
	done := startControlledAccept(t, srv, ln)
	r := bufio.NewReader(client)
	if line, err := r.ReadString('\n'); err != nil || !strings.HasPrefix(line, "+OK") {
		t.Fatalf("greeting after Accept error = %q, %v", line, err)
	}
	clientLogin(t, client, r)
	if _, err := io.WriteString(client, "QUIT\r\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := r.ReadString('\n'); err != nil || !strings.HasPrefix(line, "+OK") {
		t.Fatalf("QUIT after Accept error = %q, %v", line, err)
	}
	if b, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("after QUIT = %q, %v; want EOF", b, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown after Accept recovery: %v", err)
	}
	<-done // joins the goroutine before inspecting its Accept counter
	if calls != 3 {
		t.Errorf("Accept calls = %d, want error, connection, shutdown error", calls)
	}
	limiter.checkBalanced(t)
}

// Order the shutdown window explicitly: Accept has acquired the connection,
// then the public Close completes, then Accept returns it. No session may start;
// the connection must close without a greeting and its limiter slot be released.
func TestAcceptConnectionAfterShutdown(t *testing.T) {
	client, conn := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() { _ = conn.Close() })
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	limiter := &acceptTestLimiter{}
	srv := New(newMockBackend(), nil, limiter)
	ln := &controlledListener{closed: make(chan struct{})}
	acquired := make(chan struct{})
	allowReturn := make(chan struct{})
	releaseAccept := sync.OnceFunc(func() { close(allowReturn) })
	defer releaseAccept()
	calls := 0
	ln.accept = func() (net.Conn, error) {
		calls++
		if calls == 1 {
			close(acquired)
			<-ln.closed
			<-allowReturn
			return acceptTestConn{conn}, nil
		}
		return nil, net.ErrClosed
	}
	done := startControlledAccept(t, srv, ln)
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not acquire the connection")
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	releaseAccept()
	var b [1]byte
	if n, err := client.Read(b[:]); n != 0 || err != io.EOF {
		t.Fatalf("connection returned after Close = %q, %v; want EOF without greeting", b[:n], err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not stop after refusing a late connection")
	}
	if calls != 1 {
		t.Errorf("Accept calls = %d, want no retry after late connection", calls)
	}
	limiter.checkBalanced(t)
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.conns) != 0 {
		t.Errorf("tracked connections after Close = %d, want 0", len(srv.conns))
	}
}
