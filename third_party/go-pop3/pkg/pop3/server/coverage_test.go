package server

import (
	"bufio"
	"crypto/tls"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-pop3/pkg/pop3"
)

// The tests in this file cover failure paths the transcript tests do not reach:
// a store that cannot return a message, a client banned by its failed PASS, a
// connection the limiter refuses at accept time, a port already in use, and the
// no-op limiter itself.

// A message the store cannot retrieve is answered -ERR by RETR and TOP, and the
// session carries on.
func TestPOP3_RetrieveFailure(t *testing.T) {
	m := newMockBackend()
	m.seed("3", 100, "") // listed, but Retrieve has no bytes for it
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("RETR 1"); got != "-ERR Failed to retrieve message" {
		t.Errorf("RETR 1 = %q, want -ERR Failed to retrieve message", got)
	}
	if got := h.cmd("TOP 1 0"); got != "-ERR Failed to retrieve message" {
		t.Errorf("TOP 1 0 = %q, want -ERR Failed to retrieve message", got)
	}
	if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("NOOP after a failed retrieve = %q, want +OK", got)
	}
	if m.mbox.wasMarkedSeen("3") {
		t.Errorf("a failed RETR marked message 3 as seen")
	}
}

// banOnFailLimiter bans an address as soon as one authentication failure is
// recorded for it.
type banOnFailLimiter struct {
	pop3.NopLimiter
	banned bool
}

func (l *banOnFailLimiter) RecordAuthFail(string) { l.banned = true }
func (l *banOnFailLimiter) IsBanned(string) bool  { return l.banned }

// A failed PASS that earns the client a ban ends the session: the reply says
// why, and the server closes the connection.
func TestPOP3_FailedPassThatBansClosesSession(t *testing.T) {
	m := newMockBackend()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		NewSession(server, m, nil, &banOnFailLimiter{}).Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	h := &pop3Harness{t: t, mock: m, conn: client, cr: bufio.NewReader(client), cw: bufio.NewWriter(client), done: done}

	if g := h.readLine(); !strings.HasPrefix(g, "+OK") {
		t.Fatalf("greeting = %q", g)
	}
	if got := h.cmd("USER %s", m.user); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("USER = %q", got)
	}
	if got := h.cmd("PASS wrong"); got != "-ERR [AUTH] Too many authentication failures" {
		t.Fatalf("PASS = %q, want -ERR [AUTH] Too many authentication failures", got)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session still open after the ban")
	}
}

// refuseLimiter refuses every connection.
type refuseLimiter struct{ pop3.NopLimiter }

func (refuseLimiter) Accept(string) bool { return false }

// A connection the limiter refuses is closed before the greeting.
func TestPOP3_LimiterRefusesAtAccept(t *testing.T) {
	m := newMockBackend()
	srv := New(m, nil, refuseLimiter{})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		srv.acceptLoop(ln, false)
	}()
	t.Cleanup(func() {
		close(srv.shutdown)
		_ = ln.Close()
		<-accepted
	})

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, readErr := conn.Read(buf)
	if readErr == nil {
		t.Fatalf("refused connection got %q, want it closed", buf[:n])
	}
}

// A port that is already bound makes ListenAndServe fail and name the port,
// for both the plaintext and the implicit-TLS listener.
func TestListenAndServePortInUse(t *testing.T) {
	held, err := net.Listen("tcp", ":0") // every interface, like ListenAndServe
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })
	port := held.Addr().(*net.TCPAddr).Port

	tlsConfig := &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return nil, nil
	}}
	for name, ports := range map[string]Ports{
		"POP3":    {POP3: port},
		"POP3TLS": {POP3TLS: port},
	} {
		t.Run(name, func(t *testing.T) {
			srv := New(newMockBackend(), tlsConfig, nil)
			t.Cleanup(func() { _ = srv.Close() })
			err := srv.ListenAndServe(ports)
			want := "failed to listen on port " + strconv.Itoa(port)
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("ListenAndServe on a bound port = %v, want %q...", err, want)
			}
		})
	}
}

// NopLimiter accepts everything, bans nothing, and its bookkeeping methods do
// nothing.
func TestNopLimiter(t *testing.T) {
	var l pop3.NopLimiter
	if !l.Accept("192.0.2.1") {
		t.Error("Accept = false, want true")
	}
	l.RecordAuthFail("192.0.2.1")
	if l.IsBanned("192.0.2.1") {
		t.Error("IsBanned after RecordAuthFail = true, want false")
	}
	l.ResetAuth("192.0.2.1")
	l.Release("192.0.2.1")
}

// An empty command line gets an error without consuming the next command.
func TestPOP3_EmptyLineRejected(t *testing.T) {
	m := newMockBackend()
	h := newPOP3Harness(t, m)
	if got := h.cmd(""); !strings.HasPrefix(got, "-ERR") {
		t.Fatalf("empty command = %q, want -ERR", got)
	}
	if got := h.cmd("USER %s", m.user); !strings.HasPrefix(got, "+OK") {
		t.Fatalf("reply after an empty line = %q, want USER's +OK", got)
	}
}
