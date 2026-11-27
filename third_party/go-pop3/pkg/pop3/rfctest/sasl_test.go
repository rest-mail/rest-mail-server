package rfctest

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// saslBackend accepts SASL PLAIN for alice/secret and records every
// AuthenticatePlain call, so tests can prove malformed or refused exchanges
// never reach it. USER/PASS keeps working through the embedded backend.
type saslBackend struct {
	backend
	mu    sync.Mutex
	calls [][3]string
}

func (b *saslBackend) AuthenticatePlain(authzid, authcid, pass string, _ bool) (pop3.Mailbox, error) {
	b.mu.Lock()
	b.calls = append(b.calls, [3]string{authzid, authcid, pass})
	b.mu.Unlock()
	if authzid != "" && authzid != authcid {
		return nil, errors.New("cannot act as another user")
	}
	return b.Authenticate(authcid, pass)
}

func (b *saslBackend) expectCalls(t *testing.T, want ...[3]string) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) != len(want) {
		t.Fatalf("AuthenticatePlain calls = %q, want %q", b.calls, want)
	}
	for i := range want {
		if b.calls[i] != want[i] {
			t.Fatalf("AuthenticatePlain calls = %q, want %q", b.calls, want)
		}
	}
}

var _ pop3.SASLBackend = (*saslBackend)(nil)

// plain encodes an RFC 4616 PLAIN message.
func plain(authzid, authcid, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(authzid + "\x00" + authcid + "\x00" + pass))
}

// saslConnect runs a session with optional TLS and limiter over net.Pipe.
func saslConnect(t *testing.T, b pop3.Backend, config *tls.Config, limiter pop3.Limiter) *wire {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); pop3server.NewSession(server, b, config, limiter).Handle() }()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("session did not stop")
		}
	})
	w := &wire{t: t, conn: client, reader: bufio.NewReader(client), done: done}
	w.status(w.line(), "+OK")
	return w
}

// saslCapa returns the CAPA lines whose tag is SASL, case-insensitively.
func saslCapa(t *testing.T, w *wire) []string {
	t.Helper()
	w.status(w.cmd("CAPA"), "+OK")
	var sasl []string
	for i := 0; ; i++ {
		if i > 32 {
			t.Fatal("CAPA missing terminator")
		}
		line := w.line()
		if line == "." {
			return sasl
		}
		if tag, _, _ := strings.Cut(line, " "); strings.EqualFold(tag, "SASL") {
			sasl = append(sasl, line)
		}
	}
}

func expectSASL(t *testing.T, got []string, offered bool) {
	t.Helper()
	if offered && (len(got) != 1 || got[0] != "SASL PLAIN") {
		t.Fatalf("CAPA SASL lines = %q, want exactly [SASL PLAIN]", got)
	}
	if !offered && len(got) != 0 {
		t.Fatalf("CAPA SASL lines = %q, want none", got)
	}
}

// RFC 2449 §6.3, RFC 5034 §3: a server supporting AUTH lists SASL with its
// mechanisms in both states. Only a SASLBackend offers it, and only while
// AUTH PLAIN is accepted: with a TLS config, not before STLS.
// checklist: POP3-CAPA-08 POP3-SASL-01 POP3-CAPA-12 POP3-SASL-10 POP3-SASL-17 POP3-STLS-18 POP3-PLAIN-09
func TestSASLCapability(t *testing.T) {
	t.Run("without-backend", func(t *testing.T) {
		w := connect(t, backend{password: "secret"})
		expectSASL(t, saslCapa(t, w), false)
		w.login("secret")
		expectSASL(t, saslCapa(t, w), false)
	})
	t.Run("no-tls", func(t *testing.T) {
		w := connect(t, &saslBackend{backend: backend{password: "secret"}})
		expectSASL(t, saslCapa(t, w), true)
		w.login("secret")
		expectSASL(t, saslCapa(t, w), true)
	})
	t.Run("stls", func(t *testing.T) {
		server, client := stlsConfigs(t)
		w := saslConnect(t, &saslBackend{backend: backend{password: "secret"}}, server, nil)
		expectSASL(t, saslCapa(t, w), false)
		stlsUpgrade(t, w, client)
		expectSASL(t, saslCapa(t, w), true)
		expect(t, w.cmd("AUTH PLAIN "+plain("", "alice", "secret")), "+OK Authentication successful")
		expectSASL(t, saslCapa(t, w), true)
	})
}

// RFC 5034 §4: AUTH names a mechanism and may carry an initial response;
// without one the server sends an empty "+ " challenge and reads the response
// line. RFC 4616 §2 carries authzid, authcid and password. Success enters
// TRANSACTION. Keywords and mechanism names are case-insensitive.
// checklist: POP3-SASL-02 POP3-SASL-03 POP3-SASL-11 POP3-SASL-19
func TestSASLPlainExchange(t *testing.T) {
	for _, tc := range []struct {
		name, authzid string
		initial       bool
		command       string
	}{
		{"initial-response", "", true, "AUTH PLAIN"},
		{"challenge", "", false, "AUTH PLAIN"},
		{"mixed-case", "", true, "aUtH pLaIn"},
		{"authzid", "alice", false, "AUTH PLAIN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &saslBackend{backend: backend{password: "secret"}}
			w := connect(t, b)
			expect(t, w.cmd("STAT"), "-ERR Not authenticated")
			response := plain(tc.authzid, "alice", "secret")
			if tc.initial {
				expect(t, w.cmd(tc.command+" "+response), "+OK Authentication successful")
			} else {
				expect(t, w.cmd(tc.command), "+ ")
				expect(t, w.cmd(response), "+OK Authentication successful")
			}
			expect(t, w.cmd("STAT"), "+OK 1 42")
			b.expectCalls(t, [3]string{tc.authzid, "alice", "secret"})
			// RFC 5034 §4: AUTH is only valid in AUTHORIZATION state.
			expect(t, w.cmd("AUTH PLAIN "+response), "-ERR Already authenticated")
			b.expectCalls(t, [3]string{tc.authzid, "alice", "secret"})
		})
	}
}

// RFC 5034 §4: "*" cancels the exchange with -ERR; a response that does not
// base64-decode rejects the command; an unknown mechanism is refused. A
// malformed RFC 4616 message is refused too. None of these reach the
// Backend or end AUTHORIZATION, and a fresh AUTH can still succeed.
// checklist: POP3-SASL-04 POP3-SASL-05 POP3-SASL-12 POP3-PLAIN-06
func TestSASLPlainRejections(t *testing.T) {
	for _, tc := range []struct {
		name, command, response, reply string
	}{
		{"cancel", "AUTH PLAIN", "*", "-ERR Authentication cancelled"},
		{"bad-base64-challenge", "AUTH PLAIN", "not base64!", "-ERR Invalid base64 response"},
		{"bad-base64-initial", "AUTH PLAIN %%%", "", "-ERR Invalid base64 response"},
		{"empty-initial", "AUTH PLAIN =", "", "-ERR Invalid PLAIN response"},
		{"empty-challenge-line", "AUTH PLAIN", "", "-ERR Invalid PLAIN response"},
		{"two-fields", "AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("alice\x00secret")), "", "-ERR Invalid PLAIN response"},
		{"four-fields", "AUTH PLAIN " + plain("", "alice", "secret\x00x"), "", "-ERR Invalid PLAIN response"},
		{"empty-authcid", "AUTH PLAIN " + plain("", "", "secret"), "", "-ERR Invalid PLAIN response"},
		{"empty-password", "AUTH PLAIN " + plain("", "alice", ""), "", "-ERR Invalid PLAIN response"},
		{"invalid-utf8", "AUTH PLAIN " + plain("", "alice", "\xff"), "", "-ERR Invalid PLAIN response"},
		{"no-mechanism", "AUTH", "", "-ERR Mechanism required"},
		{"unknown-mechanism", "AUTH CRAM-MD5", "", "-ERR Unsupported mechanism"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &saslBackend{backend: backend{password: "secret"}}
			w := connect(t, b)
			if strings.HasSuffix(tc.command, "PLAIN") {
				expect(t, w.cmd(tc.command), "+ ")
				expect(t, w.cmd(tc.response), tc.reply)
			} else {
				expect(t, w.cmd(tc.command), tc.reply)
			}
			b.expectCalls(t)
			expect(t, w.cmd("STAT"), "-ERR Not authenticated")
			expect(t, w.cmd("AUTH PLAIN "+plain("", "alice", "secret")), "+OK Authentication successful")
		})
	}
}

// RFC 3206 §5: a credential failure carries AUTH. Like PASS, it is recorded
// with the Limiter, and the failure that earns a ban closes the session.
// checklist: POP3-SASL-06 POP3-SASL-12 POP3-RESP-CODES-10
func TestSASLPlainCredentialFailures(t *testing.T) {
	limiter := &authRespBanLimiter{limit: 3}
	b := &saslBackend{backend: backend{password: "secret"}}
	w := saslConnect(t, b, nil, limiter)
	expect(t, w.cmd("AUTH PLAIN "+plain("", "alice", "wrong")), "-ERR [AUTH] Authentication failed")
	expect(t, w.cmd("AUTH PLAIN "+plain("bob", "alice", "secret")), "-ERR [AUTH] Authentication failed")
	if limiter.fails != 2 {
		t.Fatalf("RecordAuthFail calls = %d, want 2", limiter.fails)
	}
	expect(t, w.cmd("AUTH PLAIN"), "+ ")
	expect(t, w.cmd(plain("", "unknown", "secret")), "-ERR [AUTH] Too many authentication failures")
	if _, err := w.reader.ReadString('\n'); err == nil {
		t.Fatal("session stayed open after the ban")
	}
	b.expectCalls(t, [3]string{"", "alice", "wrong"}, [3]string{"bob", "alice", "secret"}, [3]string{"", "unknown", "secret"})
}

// RFC 5034 §§4, 6 and RFC 2595 §2.2: PLAIN sends the password in the clear,
// so with a TLS config AUTH is refused before STLS, exactly like USER, and
// the Backend is never called. After STLS it succeeds.
// checklist: POP3-SASL-07 POP3-SASL-16 POP3-SASL-17 POP3-STLS-11 POP3-STLS-13 POP3-STLS-18 POP3-PLAIN-09 POP3-ITLS-12
func TestSASLPlainRequiresTLS(t *testing.T) {
	server, client := stlsConfigs(t)
	b := &saslBackend{backend: backend{password: "secret"}}
	w := saslConnect(t, b, server, nil)
	expect(t, w.cmd("AUTH PLAIN "+plain("", "alice", "secret")), "-ERR TLS required")
	expect(t, w.cmd("AUTH PLAIN"), "-ERR TLS required")
	expect(t, w.cmd("USER alice"), "-ERR TLS required")
	b.expectCalls(t)
	stlsUpgrade(t, w, client)
	expect(t, w.cmd("AUTH PLAIN"), "+ ")
	expect(t, w.cmd(plain("", "alice", "secret")), "+OK Authentication successful")
	expect(t, w.cmd("STAT"), "+OK 1 42")
}

// Library policy: the response line after the "+ " challenge is capped at the
// largest PLAIN response (1026 octets with CRLF, POP3-SASL-08), so an
// unterminated over-long one is refused and closes the session; a client that disconnects at the challenge ends the session.
// Neither reaches the Backend.
// checklist: POP3-SASL-POLICY-01
func TestSASLPlainResponseLine(t *testing.T) {
	t.Run("too-long", func(t *testing.T) {
		b := &saslBackend{backend: backend{password: "secret"}}
		w := connect(t, b)
		expect(t, w.cmd("AUTH PLAIN"), "+ ")
		// net.Pipe is unbuffered: write two full 1024-octet buffers without
		// an LF from another goroutine, then read the refusal.
		go func() { _, _ = io.WriteString(w.conn, strings.Repeat("A", 2048)) }()
		expect(t, w.line(), "-ERR Command line too long")
		if _, err := w.reader.ReadString('\n'); err == nil {
			t.Fatal("session stayed open after an over-long response")
		}
		b.expectCalls(t)
	})
	t.Run("disconnect", func(t *testing.T) {
		b := &saslBackend{backend: backend{password: "secret"}}
		w := connect(t, b)
		expect(t, w.cmd("AUTH PLAIN"), "+ ")
		_ = w.conn.Close()
		select {
		case <-w.done:
		case <-time.After(5 * time.Second):
			t.Fatal("session did not stop after the client disconnected")
		}
		b.expectCalls(t)
	})
}
