package rfctest

import (
	"slices"
	"strings"
	"testing"
)

// TestGreeting checks the connection opens with an untagged OK greeting, which
// leaves the client in the not authenticated state.
//
// checklist: IMAP-GREET-01
// checklist: IMAP2-CORE-21
func TestGreeting(t *testing.T) {
	c := dial(t)
	if g := c.line(); !strings.HasPrefix(g, "* OK ") {
		t.Fatalf("greeting %q, want * OK", g)
	}
	if _, tagged := c.command("a1", "SELECT INBOX"); strings.HasPrefix(tagged, "a1 OK") {
		t.Fatalf("SELECT before LOGIN: %q, want the not authenticated state to refuse it", tagged)
	}
}

// TestCapability checks CAPABILITY answers with exactly one untagged CAPABILITY
// response listing IMAP4rev1, before the tagged OK, in every state.
//
// checklist: IMAP-CAP-01
func TestCapability(t *testing.T) {
	c := dial(t)
	c.line()
	check := func(tag string) {
		t.Helper()
		untagged, tagged := c.command(tag, "CAPABILITY")
		if !strings.HasPrefix(tagged, tag+" OK") {
			t.Fatalf("%s: %q, want OK", tag, tagged)
		}
		var caps []string
		for _, l := range untagged {
			if rest, ok := strings.CutPrefix(l, "* CAPABILITY "); ok {
				if caps != nil {
					t.Fatalf("%s: more than one CAPABILITY response: %q", tag, untagged)
				}
				caps = strings.Fields(rest)
			}
		}
		if !slices.Contains(caps, "IMAP4rev1") {
			t.Fatalf("%s: capabilities %q lack IMAP4rev1", tag, caps)
		}
	}
	check("a1")
	c.login()
	check("a2")
	c.command("a3", "SELECT INBOX")
	check("a4")
}

// TestNoop checks NOOP always succeeds: not authenticated, authenticated and
// selected.
//
// checklist: IMAP-NOOP-01
func TestNoop(t *testing.T) {
	c := dial(t)
	c.line()
	if _, tagged := c.command("a1", "NOOP"); !strings.HasPrefix(tagged, "a1 OK") {
		t.Fatalf("not authenticated: %q", tagged)
	}
	c.login()
	if _, tagged := c.command("a2", "noop"); !strings.HasPrefix(tagged, "a2 OK") {
		t.Fatalf("authenticated: %q", tagged)
	}
	if _, tagged := c.command("a3", "SELECT INBOX"); !strings.HasPrefix(tagged, "a3 OK") {
		t.Fatalf("SELECT: %q", tagged)
	}
	if _, tagged := c.command("a4", "NOOP"); !strings.HasPrefix(tagged, "a4 OK") {
		t.Fatalf("selected: %q", tagged)
	}
}

// TestLogout checks LOGOUT sends an untagged BYE before the tagged OK and then
// closes the connection, both before and after authentication.
//
// checklist: IMAP-LOGOUT-01 IMAP-RESP-13
// checklist: IMAP2-CORE-23
// checklist: IMAP2-CMD-47
func TestLogout(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		c := dial(t)
		c.line()
		if authenticated {
			c.login()
		}
		untagged, tagged := c.command("a1", "LOGOUT")
		if len(untagged) != 1 || !strings.HasPrefix(untagged[0], "* BYE") {
			t.Fatalf("authenticated=%v: untagged %q, want one * BYE", authenticated, untagged)
		}
		if !strings.HasPrefix(tagged, "a1 OK") {
			t.Fatalf("authenticated=%v: %q, want OK", authenticated, tagged)
		}
		if !c.closed() {
			t.Fatalf("authenticated=%v: connection still open after LOGOUT", authenticated)
		}
	}
}

// TestLogin checks LOGIN: wrong credentials get NO and leave the session not
// authenticated; the right ones get OK and enter the authenticated state.
//
// checklist: IMAP-LOGIN-01
func TestLogin(t *testing.T) {
	c := dial(t)
	c.line()
	if _, tagged := c.command("a1", "LOGIN %s wrong", testUser); !strings.HasPrefix(tagged, "a1 NO") {
		t.Fatalf("bad password: %q, want NO", tagged)
	}
	if _, tagged := c.command("a2", "LIST \"\" *"); strings.HasPrefix(tagged, "a2 OK") {
		t.Fatalf("LIST after failed LOGIN: %q, want it refused", tagged)
	}
	if _, tagged := c.command("a3", "LOGIN %s %s", testUser, testPass); !strings.HasPrefix(tagged, "a3 OK") {
		t.Fatalf("good password: %q, want OK", tagged)
	}
	if c.backend.calls != 2 {
		t.Fatalf("Authenticate called %d times, want 2", c.backend.calls)
	}
	untagged, tagged := c.command("a4", "LIST \"\" *")
	if !strings.HasPrefix(tagged, "a4 OK") || len(untagged) == 0 {
		t.Fatalf("LIST after LOGIN: %q %q, want the authenticated state", untagged, tagged)
	}
}

// TestTaggedCompletion checks each completion result carries the tag of the
// command it completes, in command order, and an unknown command is BAD.
//
// checklist: IMAP-TAG-01 IMAP-BAD-01 IMAP-RESP-01 IMAP-SYNTAX-02
func TestTaggedCompletion(t *testing.T) {
	c := dial(t)
	c.line()
	for _, tag := range []string{"A001", "x.7", "a]1"} {
		if _, tagged := c.command(tag, "NOOP"); !strings.HasPrefix(tagged, tag+" OK") {
			t.Fatalf("%s: %q", tag, tagged)
		}
	}
	if _, tagged := c.command("b1", "FROBNICATE"); !strings.HasPrefix(tagged, "b1 BAD") {
		t.Fatalf("unknown command: %q, want BAD", tagged)
	}
}
