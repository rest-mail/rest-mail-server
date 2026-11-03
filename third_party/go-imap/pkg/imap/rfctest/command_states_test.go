package rfctest

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// TestListReference covers RFC 3501 §6.3.8: a non-empty reference is prefixed
// to the mailbox pattern, a pattern starting at the hierarchy root ignores the
// reference, and an empty pattern returns the reference's root name.
//
// checklist: IMAP-LIST-04 IMAP-LIST-05
// checklist: IMAP2-CORE-32
// checklist: IMAP2-CMD-95 IMAP2-CMD-97 IMAP2-CMD-99
// checklist: IMAP2-RESP-188
func TestListReference(t *testing.T) {
	for _, kind := range []string{"LIST", "LSUB"} {
		t.Run(kind, func(t *testing.T) {
			c := dialMailbox(t, hierarchyMailbox{})
			c.line()
			c.login()

			for _, tc := range []struct {
				reference, pattern string
				want               string
			}{
				{"Work/", "%", "Work/Q1"},
				{"Work/", "*", "Work/Q1"},
				{"Work", "*", "Work Work/Q1"},
				{"Work/", "Q1", "Work/Q1"},
				{"Work/", "INBOX", ""},
				{"Nothing/", "*", ""},
				{"Work/", "/*", ""},
			} {
				untagged, tagged := c.command("a1", `%s "%s" "%s"`, kind, tc.reference, tc.pattern)
				if tagged != "a1 OK "+kind+" completed" {
					t.Fatalf("%s %q %q: tagged %q", kind, tc.reference, tc.pattern, tagged)
				}
				if got := strings.Join(listNames(t, kind, untagged), " "); got != tc.want {
					t.Errorf("%s %q %q: names %q, want %q", kind, tc.reference, tc.pattern, got, tc.want)
				}
			}

			for _, tc := range []struct{ reference, want string }{
				{"Work/Q1", `* ` + kind + ` (\Noselect) "/" "Work/"`},
				{"Work", `* ` + kind + ` (\Noselect) "/" ""`},
			} {
				untagged, tagged := c.command("a2", `%s "%s" ""`, kind, tc.reference)
				if tagged != "a2 OK "+kind+" completed" || len(untagged) != 1 || untagged[0] != tc.want {
					t.Errorf(`%s %q "": got %q %q, want %q`, kind, tc.reference, untagged, tagged, tc.want)
				}
			}
		})
	}
}

// plainResponse is the base64 SASL PLAIN response for user and pass.
func plainResponse(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pass))
}

// authenticate sends AUTHENTICATE mech, answers its continuation with resp and
// returns the tagged reply. It fails if no "+ " continuation arrives.
func (c *client) authenticate(tag, mech, resp string) string {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, "%s AUTHENTICATE %s\r\n", tag, mech); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	if l := c.line(); l != "+ " {
		c.t.Fatalf("AUTHENTICATE %s: got %q, want an empty continuation", mech, l)
	}
	if _, err := fmt.Fprintf(c.conn, "%s\r\n", resp); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	return c.line()
}

// TestAuthenticateCommand covers RFC 3501 §6.2.2: AUTHENTICATE PLAIN is
// implemented and advertised, a continuation carries the exchange, success
// enters the authenticated state, a "*" cancel is BAD, rejected credentials and
// unsupported mechanisms are NO, and a missing mechanism is BAD. Success
// carries a CAPABILITY response code. (An initial response on the command
// line is RFC 4959's, TestSASLIR.) Failures leave the session not authenticated and able to
// retry.
//
// An authorization identity naming another user is refused; one equal to the
// authentication identity is accepted.
//
// checklist: IMAP-AUTHENTICATE-01 IMAP-AUTHENTICATE-02 IMAP-AUTHENTICATE-03 IMAP-AUTHENTICATE-04 IMAP-AUTHENTICATE-05 IMAP-AUTHENTICATE-06 IMAP-AUTHENTICATE-08 IMAP-CAP-02
// checklist: IMAP2-CMD-51 IMAP2-CMD-52 IMAP2-CMD-56 IMAP2-CMD-58 IMAP2-CMD-59
func TestAuthenticateCommand(t *testing.T) {
	c := dial(t)
	c.line()

	untagged, tagged := c.command("a0", "CAPABILITY")
	if !strings.HasPrefix(tagged, "a0 OK") || len(untagged) != 1 || !strings.Contains(untagged[0], " AUTH=PLAIN") {
		t.Fatalf("CAPABILITY: %q %q, want AUTH=PLAIN advertised", untagged, tagged)
	}

	selectRefused := func(tag string) {
		t.Helper()
		if _, tagged := c.command(tag, "SELECT INBOX"); strings.HasPrefix(tagged, tag+" OK") {
			t.Fatalf("SELECT after failed AUTHENTICATE: %q, want refusal", tagged)
		}
	}

	if got := c.authenticate("a1", "PLAIN", "*"); !strings.HasPrefix(got, "a1 BAD") {
		t.Errorf("cancelled AUTHENTICATE: %q, want tagged BAD", got)
	}
	selectRefused("s1")

	if got := c.authenticate("a2", "PLAIN", plainResponse(testUser, "wrong")); !strings.HasPrefix(got, "a2 NO") {
		t.Errorf("wrong credentials: %q, want tagged NO", got)
	}
	selectRefused("s2")

	if _, tagged := c.command("a3", "AUTHENTICATE X-UNKNOWN"); !strings.HasPrefix(tagged, "a3 NO") {
		t.Errorf("unsupported mechanism: %q, want tagged NO without a continuation", tagged)
	}
	if _, tagged := c.command("a4", "AUTHENTICATE"); !strings.HasPrefix(tagged, "a4 BAD") {
		t.Errorf("missing mechanism: %q, want tagged BAD", tagged)
	}
	other := base64.StdEncoding.EncodeToString([]byte("bob@example.com\x00" + testUser + "\x00" + testPass))
	if got := c.authenticate("a7", "PLAIN", other); !strings.HasPrefix(got, "a7 NO") {
		t.Errorf("authorization identity of another user: %q, want tagged NO", got)
	}
	selectRefused("s4")

	same := base64.StdEncoding.EncodeToString([]byte(testUser + "\x00" + testUser + "\x00" + testPass))
	if got := c.authenticate("a5", "plain", same); !strings.HasPrefix(got, "a5 OK [CAPABILITY IMAP4rev1 ") {
		t.Fatalf("AUTHENTICATE plain: %q, want tagged OK", got)
	}
	if _, tagged := c.command("a6", "SELECT INBOX"); !strings.HasPrefix(tagged, "a6 OK") {
		t.Fatalf("SELECT after AUTHENTICATE: %q", tagged)
	}
}

// TestNoReauthentication covers RFC 3501 §6.2: LOGIN and AUTHENTICATE are valid
// only in the not authenticated state. Once authenticated, both get BAD without
// a continuation or a credential check, and the session keeps its user and its
// selected mailbox (§6: a BAD never changes state).
//
// checklist: IMAP-AUTH-STATE-01
func TestNoReauthentication(t *testing.T) {
	c := dial(t)
	b := c.backend
	c.line()
	c.login()
	if _, tagged := c.command("a1", "SELECT INBOX"); !strings.HasPrefix(tagged, "a1 OK") {
		t.Fatalf("SELECT: %q", tagged)
	}
	calls := b.calls

	for _, cmd := range []string{
		"LOGIN " + testUser + " " + testPass,
		"LOGIN bob@example.com wrong",
		"AUTHENTICATE PLAIN",
	} {
		if _, tagged := c.command("a2", "%s", cmd); !strings.HasPrefix(tagged, "a2 BAD") {
			t.Errorf("%s when authenticated: %q, want tagged BAD", cmd, tagged)
		}
	}
	if b.calls != calls {
		t.Errorf("backend Authenticate called %d more times, want none", b.calls-calls)
	}
	if untagged, tagged := c.command("a3", "FETCH 1 FLAGS"); !strings.HasPrefix(tagged, "a3 OK") || len(untagged) != 1 {
		t.Errorf("FETCH after refused re-authentication: %q %q, want the selection kept", untagged, tagged)
	}
}

// TestCheckCommand covers RFC 3501 §6.4.1: CHECK is a selected-state command
// that takes no arguments. Without housekeeping it is equivalent to NOOP, so it
// succeeds under SELECT and EXAMINE; before selection, or with an argument, it
// gets BAD.
//
// checklist: IMAP-CHECK-01 IMAP-CHECK-02
func TestCheckCommand(t *testing.T) {
	c := dial(t)
	c.line()
	if _, tagged := c.command("a1", "CHECK"); !strings.HasPrefix(tagged, "a1 BAD") {
		t.Errorf("CHECK before authentication: %q, want BAD", tagged)
	}
	c.login()
	if _, tagged := c.command("a2", "CHECK"); !strings.HasPrefix(tagged, "a2 BAD") {
		t.Errorf("CHECK with no mailbox selected: %q, want BAD", tagged)
	}
	for _, sel := range []string{"SELECT", "EXAMINE"} {
		if _, tagged := c.command("a3", "%s INBOX", sel); !strings.HasPrefix(tagged, "a3 OK") {
			t.Fatalf("%s: %q", sel, tagged)
		}
		if _, tagged := c.command("a4", "CHECK"); tagged != "a4 OK CHECK completed" {
			t.Errorf("CHECK after %s: %q, want OK", sel, tagged)
		}
		if _, tagged := c.command("a5", "CHECK now"); !strings.HasPrefix(tagged, "a5 BAD") {
			t.Errorf("CHECK with an argument after %s: %q, want BAD", sel, tagged)
		}
	}
	if _, tagged := c.command("a6", "FETCH 1 FLAGS"); !strings.HasPrefix(tagged, "a6 OK") {
		t.Errorf("FETCH after CHECK: %q, want the selection kept", tagged)
	}
}
