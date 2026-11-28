package server

import (
	"bufio"
	"crypto/tls"
	"net"
	"strings"
	"testing"

	"github.com/rest-mail/go-pop3/pkg/pop3"
)

// TestPOP3_NoArgCommandsRejectSpuriousArgs is the regression guard for issue
// #14. RFC 1939 §3 defines STAT, RSET, NOOP, and QUIT — and STLS (RFC 2595) and
// CAPA (RFC 2449) — as taking no arguments. A command line that carries a
// trailing token is therefore a syntax error and must be answered -ERR, not
// silently accepted. Against the old code these commands ignored the junk and
// still replied +OK (or, for QUIT, signed off), so this test fails; the fixed
// dispatch rejects the extra argument.
func TestPOP3_NoArgCommandsRejectSpuriousArgs(t *testing.T) {
	// Commands that are dispatched authenticated. STLS/CAPA are covered
	// separately (STLS needs a TLS config; CAPA's positive form is multi-line).
	for _, cmd := range []string{"STAT", "RSET", "NOOP", "QUIT", "CAPA"} {
		t.Run(cmd, func(t *testing.T) {
			m := newMockBackend()
			seedThree(m)
			h := newPOP3Harness(t, m)
			h.login()

			if got := h.cmd("%s extra", cmd); !strings.HasPrefix(got, "-ERR") {
				t.Errorf("%s extra = %q, want -ERR (no-argument command must reject a trailing token)", cmd, got)
			}

			// Rejecting the malformed line must not tear down the session: even
			// QUIT with a spurious argument stays in the current state rather than
			// entering UPDATE, so a follow-up command still gets its own reply.
			if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
				t.Errorf("session unusable after rejected %q: NOOP = %q", cmd, got)
			}
		})
	}
}

// TestPOP3_NoArgCommandsAcceptBareForm guards the other edge: the bare,
// argument-free form of each no-argument command must keep working after the
// fix, so rejecting spurious arguments does not reject legitimate traffic.
func TestPOP3_NoArgCommandsAcceptBareForm(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)
	h.login()

	if got := h.cmd("STAT"); got != "+OK 3 600" {
		t.Errorf("bare STAT = %q, want %q", got, "+OK 3 600")
	}
	if got := h.cmd("NOOP"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("bare NOOP = %q, want +OK", got)
	}
	if got := h.cmd("RSET"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("bare RSET = %q, want +OK", got)
	}
	// Bare CAPA still emits the multi-line capability list terminated by ".".
	if got := h.cmd("CAPA"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("bare CAPA header = %q, want +OK", got)
	}
	if body := h.readDotBody(); !containsLine(body, "UIDL") {
		t.Errorf("bare CAPA body missing UIDL capability: %v", body)
	}
}

// TestPOP3_ArgTakingCommandsUnaffected proves the fix is scoped to no-argument
// commands: commands that legitimately take arguments — including the optional
// argument to LIST/UIDL — still work, both with and without their argument.
func TestPOP3_ArgTakingCommandsUnaffected(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3Harness(t, m)
	h.login()

	// LIST with an optional single-message argument.
	if got := h.cmd("LIST 2"); got != "+OK 2 200" {
		t.Errorf("LIST 2 = %q, want %q", got, "+OK 2 200")
	}
	// UIDL with an optional single-message argument.
	if got := h.cmd("UIDL 3"); got != "+OK 3 20" {
		t.Errorf("UIDL 3 = %q, want %q", got, "+OK 3 20")
	}
	// LIST with no argument still lists everything.
	if got := h.cmd("LIST"); got != "+OK 3 messages (600 octets)" {
		t.Errorf("bare LIST = %q", got)
	}
	_ = h.readDotBody()
	// A message-taking command with its argument.
	if got := h.cmd("DELE 1"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("DELE 1 = %q, want +OK", got)
	}
}

// newPOP3HarnessTLS is a harness variant whose session is created with a
// non-nil *tls.Config, so STLS is advertised and reaches its handler. The
// config need not be usable: the STLS argument check runs before any TLS
// handshake is attempted.
func newPOP3HarnessTLS(t *testing.T, mock *mockBackend) *pop3Harness {
	t.Helper()
	client, server := net.Pipe()
	sess := NewSession(server, mock, &tls.Config{}, pop3.NopLimiter{}) //nolint:gosec // test config; no handshake is performed

	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()

	h := &pop3Harness{
		t:    t,
		mock: mock,
		conn: client,
		cr:   bufio.NewReader(client),
		cw:   bufio.NewWriter(client),
		done: done,
	}
	t.Cleanup(func() {
		_ = client.Close()
		<-h.done
	})

	if g := h.readLine(); !strings.HasPrefix(g, "+OK") {
		t.Fatalf("greeting = %q, want +OK...", g)
	}
	return h
}

// TestPOP3_STLSRejectsSpuriousArgs asserts STLS (RFC 2595, no arguments) rejects
// a trailing token with -ERR before any TLS negotiation begins. Against the old
// code "STLS junk" fell straight into the handler and answered "+OK Begin TLS
// negotiation"; the fixed dispatch refuses the malformed line and leaves the
// connection in cleartext AUTHORIZATION state.
func TestPOP3_STLSRejectsSpuriousArgs(t *testing.T) {
	m := newMockBackend()
	seedThree(m)
	h := newPOP3HarnessTLS(t, m)

	got := h.cmd("STLS junk")
	if !strings.HasPrefix(got, "-ERR") {
		t.Fatalf("STLS junk = %q, want -ERR (no-argument command must reject a trailing token)", got)
	}
	if strings.Contains(got, "Begin TLS") {
		t.Fatalf("STLS junk started TLS negotiation: %q", got)
	}
	// No handshake was attempted, so the plaintext session remains usable. Probe
	// with CAPA, an AUTHORIZATION-state command (RFC 2449) — NOOP is now rejected
	// before authentication (issue #11) and so cannot serve as a pre-auth liveness
	// check here.
	if got := h.cmd("CAPA"); !strings.HasPrefix(got, "+OK") {
		t.Errorf("session unusable after rejected STLS: CAPA = %q", got)
	}
	_ = h.readDotBody()
}
