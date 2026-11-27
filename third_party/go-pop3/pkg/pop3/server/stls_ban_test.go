package server

import (
	"bufio"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-pop3/pkg/pop3"
)

// TestPOP3_STLSRejectedAfterAuth is the regression guard for issue #17,
// observation 1. STLS (STARTTLS for POP3, RFC 2595) belongs to the AUTHORIZATION
// state: it may be issued only before authentication so the credential exchange
// that follows is protected by the freshly negotiated TLS layer. Once the
// session has entered TRANSACTION state the command is out of sequence and must
// be answered -ERR.
//
// The old handler gated STLS only on usingTLS and tlsConfig, never on session
// state, so on a connection that reached TRANSACTION while still cleartext it
// replied "+OK Begin TLS negotiation" and began a handshake after PASS. This
// test drives STLS from exactly that state and asserts -ERR before any
// negotiation begins; it fails against the old code and passes once handleSTLS
// checks s.auth.authenticated.
func TestPOP3_STLSRejectedAfterAuth(t *testing.T) {
	m := newMockBackend()
	seedThree(m)

	client, server := net.Pipe()
	// A non-nil config makes STLS reachable; it need not be usable because the
	// state check must refuse the command before any handshake is attempted.
	sess := NewSession(server, m, &tls.Config{}, pop3.NopLimiter{}) //nolint:gosec // test config; STLS is refused before any handshake
	// Reach TRANSACTION state on a still-cleartext connection: authenticated but
	// not TLS-upgraded (usingTLS stays false). This is the exact precondition
	// under which the old handler let STLS proceed after PASS (issue #17).
	sess.auth.authenticated = true
	sess.mailbox = m.mbox
	msgs, _ := m.mbox.Messages()
	sess.messages = msgs

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
	readLine := func() string {
		t.Helper()
		_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := cr.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v (partial %q)", err, line)
		}
		return strings.TrimRight(line, "\r\n")
	}

	if g := readLine(); !strings.HasPrefix(g, "+OK") {
		t.Fatalf("greeting = %q, want +OK...", g)
	}

	_ = client.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write([]byte("STLS\r\n")); err != nil {
		t.Fatalf("write STLS: %v", err)
	}

	got := readLine()
	if !strings.HasPrefix(got, "-ERR") {
		t.Fatalf("STLS after authentication = %q, want -ERR (STLS is AUTHORIZATION-only, RFC 2595)", got)
	}
	if strings.Contains(got, "Begin TLS") {
		t.Fatalf("STLS proceeded to TLS negotiation after authentication: %q", got)
	}
}

// banLimiter is a [pop3.Limiter] double whose ban verdict is fixed for the test. It
// records whether Accept was reached so a test can prove a banned connection is
// dropped before the session (and its per-IP connection slot) is ever created.
type banLimiter struct {
	banned   bool
	accepted bool
}

func (l *banLimiter) Accept(string) bool    { l.accepted = true; return true }
func (l *banLimiter) Release(string)        {}
func (l *banLimiter) RecordAuthFail(string) {}
func (l *banLimiter) IsBanned(string) bool  { return l.banned }
func (l *banLimiter) ResetAuth(string)      {}

// TestPOP3_BannedConnectionRejectedAtAccept is the regression guard for issue
// #17, observation 2. The failed-authentication ban used to be consulted only
// inside the PASS-failure path, so a banned IP could still open a fresh
// connection and spend one more authentication attempt on every reconnect; only
// a *failed* attempt re-checked the ban. The accept loop now consults the ban
// list up front and drops a banned client before a session is created.
//
// A banned client must therefore be closed at accept time without ever seeing
// the POP3 greeting; a non-banned client must still be served normally so the
// guard does not reject legitimate traffic. Against the old accept loop the
// banned client received "+OK ... ready", so the banned subtest fails.
func TestPOP3_BannedConnectionRejectedAtAccept(t *testing.T) {
	for _, tc := range []struct {
		name         string
		banned       bool
		wantGreeting bool
	}{
		{name: "banned", banned: true, wantGreeting: false},
		{name: "not_banned", banned: false, wantGreeting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockBackend()
			seedThree(m)
			lim := &banLimiter{banned: tc.banned}
			srv := New(m, nil, lim)

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
			gotGreeting := readErr == nil && strings.HasPrefix(string(buf[:n]), "+OK")

			if gotGreeting != tc.wantGreeting {
				t.Fatalf("greeting delivered = %v (data %q, err %v), want %v",
					gotGreeting, string(buf[:n]), readErr, tc.wantGreeting)
			}
			if tc.banned && lim.accepted {
				t.Errorf("banned IP reached limiter.Accept; ban must be enforced before the connection slot is taken")
			}
		})
	}
}
