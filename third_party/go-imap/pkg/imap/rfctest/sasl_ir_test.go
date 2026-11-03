package rfctest

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestSASLIR: RFC 4959 adds an optional initial client response to
// AUTHENTICATE, "AUTHENTICATE" SP auth-type [SP (base64 / "=")]. The server
// advertises SASL-IR beside AUTH=PLAIN. A PLAIN response on the command line
// signs in with no continuation, as in the RFC's first example, and the RFC
// 3501 exchange without one still works, as it MUST. "=" is a zero-length
// response, which PLAIN cannot use. A response that is not base64 ("=AAA",
// "AAA=BBB", a quoted string, which it "MUST be transmitted outside of") is
// handled as any client response that does not decode: NO. An empty one or
// one with a space is no argument at all: BAD. None is answered with a
// continuation, and a refusal leaves the session not authenticated.
//
// checklist: IMAP-SASLIR-01 IMAP-SASLIR-02 IMAP-SASLIR-03 IMAP-SASLIR-04 IMAP-AUTHENTICATE-07
// checklist: IMAP2-CMD-52 IMAP2-CMD-53 IMAP2-CMD-54 IMAP2-CMD-56 IMAP2-CMD-59
func TestSASLIR(t *testing.T) {
	c := dial(t)
	c.line()
	caps := c.capabilities("C01")
	requireCaps(t, caps, []string{"SASL-IR", "AUTH=PLAIN"}, nil)

	for _, tc := range []struct{ initial, want string }{
		{"=", "A1 NO "},
		{"=AAA", "A1 NO "},
		{"AAA=BBB", "A1 NO "},
		{`"` + plainResponse(testUser, testPass) + `"`, "A1 NO "},
		{plainResponse(testUser, "wrong"), "A1 NO "},
		{"", "A1 BAD "},
		{plainResponse(testUser, testPass) + " x", "A1 BAD "},
	} {
		lines, tagged := c.command("A1", "AUTHENTICATE PLAIN %s", tc.initial)
		if !strings.HasPrefix(tagged, tc.want) || len(lines) != 0 {
			t.Fatalf("AUTHENTICATE PLAIN %q: %q %q, want %s… with no continuation", tc.initial, lines, tagged, tc.want)
		}
		if _, tagged := c.command("S1", "SELECT INBOX"); strings.HasPrefix(tagged, "S1 OK") {
			t.Fatalf("AUTHENTICATE PLAIN %q left the session authenticated", tc.initial)
		}
	}

	// The RFC 4959 §4 example: the initial response on the command line, and
	// the tagged OK straight after it.
	lines, tagged := c.command("A01", "AUTHENTICATE PLAIN %s", plainResponse(testUser, testPass))
	if !strings.HasPrefix(tagged, "A01 OK ") || len(lines) != 0 {
		t.Fatalf("initial response: %q %q", lines, tagged)
	}
	if _, tagged := c.command("S2", "SELECT INBOX"); !strings.HasPrefix(tagged, "S2 OK") {
		t.Fatalf("SELECT after signing in: %q", tagged)
	}

	// The same server still takes the RFC 3501 exchange.
	c2 := dial(t)
	c2.line()
	authzid := base64.StdEncoding.EncodeToString([]byte(testUser + "\x00" + testUser + "\x00" + testPass))
	if got := c2.authenticate("A02", "PLAIN", authzid); !strings.HasPrefix(got, "A02 OK ") {
		t.Fatalf("AUTHENTICATE without an initial response: %q", got)
	}
}
