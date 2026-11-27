package rfctest

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// implicitTLSServer starts a Server whose only listener is the implicit-TLS
// ("pop3s") one, the way the gateway runs on port 995. ListenAndServe takes a
// fixed port, so the test reserves a free one and retries if another process
// takes it first. This is the one fixture in this package that opens a port.
func implicitTLSServer(t *testing.T, config *tls.Config) (string, *stlsBackend) {
	t.Helper()
	b := &stlsBackend{backend: backend{password: "secret"}}
	srv := pop3server.New(b, config, pop3.NopLimiter{})
	for attempt := 0; ; attempt++ {
		probe, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := probe.Addr().(*net.TCPAddr).Port
		_ = probe.Close()
		if err := srv.ListenAndServe(pop3server.Ports{POP3TLS: port}); err != nil {
			if attempt < 5 {
				continue
			}
			t.Fatalf("ListenAndServe: %v", err)
		}
		t.Cleanup(func() {
			_ = srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(ctx); err != nil {
				t.Errorf("implicit-TLS server did not stop: %v", err)
			}
		})
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), b
	}
}

// implicitTLSDial completes the TLS handshake before reading anything, then
// reads the greeting as TLS application data.
func implicitTLSDial(t *testing.T, addr string, config *tls.Config) *wire {
	t.Helper()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, config)
	if err != nil {
		t.Fatalf("implicit-TLS handshake: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	state := conn.ConnectionState()
	if !state.HandshakeComplete || state.Version != tls.VersionTLS13 || len(state.VerifiedChains) == 0 {
		t.Fatalf("TLS handshake/verification incomplete: %+v", state)
	}
	w := &wire{t: t, conn: conn, reader: bufio.NewReader(conn)}
	w.status(w.line(), "+OK")
	return w
}

// checklist: POP3-ITLS-01 POP3-ITLS-02 POP3-ITLS-03 POP3-ITLS-08
func TestImplicitTLSSession(t *testing.T) {
	server, client := stlsConfigs(t)
	addr, b := implicitTLSServer(t, server)
	w := implicitTLSDial(t, addr, client)

	// AUTHORIZATION, not TRANSACTION, after the greeting.
	w.status(w.cmd("STAT"), "-ERR")
	if got := b.calls.Load(); got != 0 {
		t.Fatalf("authentication calls before USER/PASS = %d, want 0", got)
	}
	// The connection is already TLS: no STLS offered, STLS refused, and the
	// session survives the refusal.
	stlsCapabilities(t, w, false)
	expect(t, w.cmd("STLS"), "-ERR Already using TLS")
	stlsCapabilities(t, w, false)
	// The TLS-required policy for USER/PASS is met without STLS.
	w.login("secret")
	expect(t, w.cmd("STAT"), "+OK 1 42")
	if got := b.calls.Load(); got != 1 {
		t.Fatalf("authentication calls = %d, want 1", got)
	}
	stlsCapabilities(t, w, false)
	w.status(w.cmd("QUIT"), "+OK")
}

// checklist: POP3-ITLS-01
func TestImplicitTLSRejectsPlaintext(t *testing.T) {
	server, _ := stlsConfigs(t)
	addr, b := implicitTLSServer(t, server)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	// A plaintext POP3 client speaks first in this exchange; its command is
	// not a TLS record, so the handshake fails and no POP3 greeting or reply
	// is ever sent in the clear.
	if _, err := io.WriteString(conn, "USER alice\r\nPASS secret\r\n"); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(conn)
	if err != nil && !isClosed(err) {
		t.Fatalf("plaintext client on implicit-TLS port: %v (read %q)", err, got)
	}
	if strings.Contains(string(got), "+OK") || strings.Contains(string(got), "-ERR") {
		t.Fatalf("plaintext POP3 bytes on implicit-TLS port: %q", got)
	}
	if got := b.calls.Load(); got != 0 {
		t.Fatalf("authentication calls = %d, want 0", got)
	}
}

// isClosed reports a peer close that surfaces as an error instead of EOF,
// such as a reset after the server aborts the failed handshake.
func isClosed(err error) bool {
	return strings.Contains(err.Error(), "connection reset")
}
