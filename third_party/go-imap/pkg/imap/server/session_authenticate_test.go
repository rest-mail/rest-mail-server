package server

import (
	"encoding/base64"
	"strings"
	"testing"
)

// The AUTHENTICATE continuation request must be the grammar-required "+" SP
// [base64-challenge] (RFC 3501 §7.5 / §6.2.2, RFC 4959) — a "+" followed by a
// space and the (here empty, for PLAIN) server challenge — not a bare "+".
func TestIMAP_AuthenticateContinuationIsPlusSpace(t *testing.T) {
	m := newMockBackend()
	h := newIMAPHarness(t, m) // nil tlsConfig, no pre-auth gate

	h.send("a1 AUTHENTICATE PLAIN")
	got := h.readLine()
	if got != "+ " {
		t.Fatalf("continuation = %q, want %q ('+' SP + empty base64 challenge)", got, "+ ")
	}
}

// A client that aborts the SASL exchange by sending a lone "*" on the
// continuation line (RFC 4959) must be answered with a tagged BAD — the server
// MUST reject the AUTHENTICATE command. Previously "*" was base64-decoded, the
// decode failed, and it was reported as a tagged NO "Invalid base64", conflating
// a deliberate abort with a bad-credential decode error.
func TestIMAP_AuthenticateAbortRepliesBAD(t *testing.T) {
	m := newMockBackend()
	h := newIMAPHarness(t, m)

	h.send("a1 AUTHENTICATE PLAIN")
	if got := h.readLine(); got != "+ " {
		t.Fatalf("continuation = %q, want %q", got, "+ ")
	}
	h.send("*") // client abort

	status := h.readLine()
	if !strings.HasPrefix(status, "a1 BAD") {
		t.Fatalf("abort response = %q, want tagged \"a1 BAD ...\" (not NO)", status)
	}
	if strings.Contains(status, "Invalid base64") {
		t.Fatalf("abort was reported as a base64 error: %q", status)
	}

	// The connection remains usable and unauthenticated: a command requiring
	// auth is still refused.
	_, sel := h.command("a2", "SELECT INBOX")
	if !strings.Contains(sel, " NO") {
		t.Fatalf("SELECT after aborted AUTHENTICATE = %q, want NO (not authenticated)", sel)
	}
}

// A successful AUTHENTICATE PLAIN exchange still completes with a tagged OK.
func TestIMAP_AuthenticateSuccessStillOK(t *testing.T) {
	m := newMockBackend()
	h := newIMAPHarness(t, m)

	h.send("a1 AUTHENTICATE PLAIN")
	if got := h.readLine(); got != "+ " {
		t.Fatalf("continuation = %q, want %q", got, "+ ")
	}
	h.send("%s", plainCredential(m.user, m.pass))
	if status := h.readLine(); !strings.HasPrefix(status, "a1 OK") {
		t.Fatalf("AUTHENTICATE success = %q, want a1 OK", status)
	}
}

// A genuine authentication failure — well-formed base64 PLAIN payload carrying
// wrong credentials — is still a tagged NO [AUTHENTICATIONFAILED], distinct from
// an abort (BAD). This guards against over-broadening the abort handling.
func TestIMAP_AuthenticateWrongCredsStillNO(t *testing.T) {
	m := newMockBackend()
	h := newIMAPHarness(t, m)

	h.send("a1 AUTHENTICATE PLAIN")
	if got := h.readLine(); got != "+ " {
		t.Fatalf("continuation = %q, want %q", got, "+ ")
	}
	h.send("%s", plainCredential(m.user, "wrong-password"))
	status := h.readLine()
	if !strings.HasPrefix(status, "a1 NO") {
		t.Fatalf("wrong-credential AUTHENTICATE = %q, want tagged NO", status)
	}
	if !strings.Contains(status, "[AUTHENTICATIONFAILED]") {
		t.Fatalf("wrong-credential AUTHENTICATE = %q, want [AUTHENTICATIONFAILED]", status)
	}
}

// Malformed base64 on the continuation line (that is NOT the abort "*") remains a
// tagged NO "Invalid base64" — abort handling must not swallow real decode errors.
func TestIMAP_AuthenticateBadBase64StillNO(t *testing.T) {
	m := newMockBackend()
	h := newIMAPHarness(t, m)

	// Sanity: the payload really is invalid base64, so the NO is about decoding.
	if _, err := base64.StdEncoding.DecodeString("!!!not-base64!!!"); err == nil {
		t.Fatal("test payload unexpectedly decoded as valid base64")
	}

	h.send("a1 AUTHENTICATE PLAIN")
	if got := h.readLine(); got != "+ " {
		t.Fatalf("continuation = %q, want %q", got, "+ ")
	}
	h.send("!!!not-base64!!!")
	status := h.readLine()
	if !strings.HasPrefix(status, "a1 NO") {
		t.Fatalf("bad-base64 AUTHENTICATE = %q, want tagged NO", status)
	}
}
