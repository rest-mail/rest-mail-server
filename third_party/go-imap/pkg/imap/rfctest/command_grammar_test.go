package rfctest

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// malformedSelected are commands that break RFC 3501 §9's grammar in the
// selected state: a missing, extra or doubled argument or space. None may run.
var malformedSelected = []string{
	// Mailbox-management commands.
	"SELECT", "EXAMINE", "CREATE", "CREATE a b", "CREATE  a", "CREATE a ",
	"DELETE", "DELETE a b", "DELETE  a", "DELETE a ",
	"RENAME", "RENAME a", "RENAME a b c", "RENAME  a b", "RENAME a  b", "RENAME a b ",
	"SUBSCRIBE", "SUBSCRIBE a b", "SUBSCRIBE  a", "SUBSCRIBE a ",
	"UNSUBSCRIBE", "UNSUBSCRIBE a b", "UNSUBSCRIBE  a", "UNSUBSCRIBE a ",
	// LIST, LSUB and STATUS.
	"LIST", `LIST ""`, `LIST "" * `, `LIST ""  *`, `LIST  "" *`, `LIST "" * extra`,
	"LSUB", `LSUB ""`, `LSUB "" * `, `LSUB ""  *`, `LSUB  "" *`, `LSUB "" * extra`,
	"STATUS", "STATUS INBOX", "STATUS INBOX ()", "STATUS INBOX (MESSAGES) extra",
	"STATUS  INBOX (MESSAGES)", "STATUS INBOX  (MESSAGES)", "STATUS INBOX ( MESSAGES)",
	"STATUS INBOX (MESSAGES )", "STATUS INBOX (MESSAGES  UNSEEN)",
	// FETCH.
	"FETCH", "FETCH 1", "FETCH  1 FLAGS", "FETCH 1  FLAGS", "FETCH 1 FLAGS ",
	"FETCH 1 FLAGS extra", "FETCH 1 ( FLAGS)", "FETCH 1 (FLAGS )", "FETCH 1 (FLAGS  UID)",
	// STORE.
	"STORE", "STORE 1", "STORE 1 FLAGS", "STORE  1 FLAGS (\\Seen)", "STORE 1  FLAGS (\\Seen)",
	"STORE 1 FLAGS  (\\Seen)", "STORE 1 FLAGS (\\Seen) extra", "STORE 1 FLAGS (\\Seen )",
	"STORE 1 FLAGS ( \\Seen)", "STORE 1 FLAGS (\\Seen  \\Draft)",
	// COPY.
	"COPY", "COPY 1", "COPY  1 INBOX", "COPY 1  INBOX", "COPY 1 INBOX extra", "COPY 1 INBOX ",
	// UID.
	"UID", "UID  FETCH 1 FLAGS", "UID FETCH", "UID FETCH 10", "UID FETCH  10 FLAGS",
	"UID FETCH 10 FLAGS extra", "UID STORE 10 FLAGS", "UID STORE 10 FLAGS (\\Seen) extra",
	"UID COPY 10", "UID COPY 10 INBOX extra", "UID SEARCH", "UID SEARCH  ALL", "UID SEARCH ALL ",
	// SEARCH.
	"SEARCH", "SEARCH  ALL", "SEARCH ALL ", "SEARCH ALL  SEEN", "SEARCH ( ALL)", "SEARCH (ALL )",
	"SEARCH (ALL", "SEARCH ALL)", "SEARCH ()", "SEARCH NOT", "SEARCH OR ALL",
	// Commands that take no arguments.
	"CAPABILITY x", "NOOP x", "CHECK x", "CLOSE x", "EXPUNGE x", "LOGOUT x", "NOOP ", "CHECK ",
}

// requireStateKept proves the selected fixture is as requireStateKept's caller
// left it: message 1 still answers with \Deleted as set, and nothing was written.
func requireStateKept(t *testing.T, c *client, box *fetchMailbox, command string, writes int, deleted bool) {
	t.Helper()
	lines, tagged := c.command("K1", "FETCH 1 (UID FLAGS)")
	if !strings.HasPrefix(tagged, "K1 OK ") || len(lines) != 1 || !strings.Contains(lines[0], "UID 10") {
		t.Fatalf("after %q the selection is gone: %q %q", command, lines, tagged)
	}
	if strings.Contains(lines[0], `\Deleted`) != deleted {
		t.Fatalf("after %q the flags changed: %q", command, lines)
	}
	if len(box.updates[10]) != writes || len(box.fetches) != 0 {
		t.Fatalf("after %q: updates=%v fetches=%v", command, box.updates, box.fetches)
	}
}

// TestMalformedCommandsAnswerBAD covers RFC 3501 §2.2.2: "Any client command
// with a protocol syntax error, including (but not limited to) missing or
// extraneous spaces or arguments, SHOULD be rejected". Each command gets one
// tagged BAD with no other response, and does not run. On a read-only selection
// a malformed STORE is BAD too, and only a well-formed one gets EXAMINE's NO.
//
// checklist: IMAP-INV-SYNTAX-01
func TestMalformedCommandsAnswerBAD(t *testing.T) {
	for _, command := range malformedSelected {
		t.Run(command, func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			lines, tagged := c.command("B1", "%s", command)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "B1 BAD ") {
				t.Fatalf("%q: %q, want BAD", command, tagged)
			}
			requireStateKept(t, c, box, command, 0, false)
			requireOK(t, secondLine(c.command("K2", "NOOP")), "K2")
		})
	}
	for _, command := range []string{"STORE 1 +FLAGS", `STORE 1 FLAGS (\Seen) extra`, `STORE 0 +FLAGS (\Seen)`, `STORE 1 =FLAGS (\Seen)`, "UID STORE 10 +FLAGS", `UID STORE x +FLAGS (\Seen)`} {
		t.Run("EXAMINE/"+command, func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "EXAMINE")
			if _, tagged := c.command("B1", "%s", command); !strings.HasPrefix(tagged, "B1 BAD ") {
				t.Fatalf("%q: %q, want BAD", command, tagged)
			}
		})
	}
	for _, command := range []string{`STORE 1 +FLAGS (\Seen)`, `UID STORE 10 +FLAGS (\Seen)`} {
		t.Run("EXAMINE/"+command, func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "EXAMINE")
			if _, tagged := c.command("N1", "%s", command); tagged != "N1 NO Mailbox is read-only" {
				t.Fatalf("%q: %q, want the read-only NO", command, tagged)
			}
		})
	}
}

func secondLine(_ []string, tagged string) string { return tagged }

// TestRejectedCommandsKeepConnectionState covers RFC 3501 §6: "A rejected
// command (BAD response) never changes the state of the connection or of the
// selected mailbox", and a failed one (NO) generally does not either, except
// SELECT and EXAMINE.
//
// checklist: IMAP-INV-STATE-01
func TestRejectedCommandsKeepConnectionState(t *testing.T) {
	t.Run("selected/BAD", func(t *testing.T) {
		for _, selection := range []string{"SELECT", "EXAMINE"} {
			box := newFetchMailbox()
			box.messages[0].Deleted = true
			c := fetchClient(t, box, selection)
			for _, command := range malformedSelected {
				if command == "LOGOUT x" {
					continue
				}
				if _, tagged := c.command("B1", "%s", command); !strings.HasPrefix(tagged, "B1 BAD ") {
					t.Fatalf("%q: %q", command, tagged)
				}
				requireStateKept(t, c, box, command, 0, true)
			}
			// CLOSE and EXPUNGE would have removed the message.
			if box.messages[0].UID != 10 {
				t.Fatalf("%s: message removed: %+v", selection, box.messages)
			}
		}
	})
	t.Run("selected/NO", func(t *testing.T) {
		box := newFetchMailbox()
		c := fetchClient(t, box, "SELECT")
		for _, command := range []string{"STATUS NIL (MESSAGES)", "DELETE INBOX", "RENAME INBOX elsewhere", `COPY 1 "a&b"`} {
			if _, tagged := c.command("N1", "%s", command); !strings.HasPrefix(tagged, "N1 NO ") {
				t.Fatalf("%q: %q, want NO", command, tagged)
			}
			requireStateKept(t, c, box, command, 0, false)
		}
		box.failUID = 10
		if _, tagged := c.command("N2", "FETCH 1 BODY.PEEK[]"); !strings.HasPrefix(tagged, "N2 NO ") {
			t.Fatalf("failed FETCH: %q", tagged)
		}
		box.failUID, box.fetches = 0, nil
		requireStateKept(t, c, box, "failed FETCH", 0, false)
	})
	t.Run("selected/NO on SELECT deselects", func(t *testing.T) {
		c := fetchClient(t, newFetchMailbox(), "SELECT")
		if _, tagged := c.command("N1", "SELECT Missing"); !strings.HasPrefix(tagged, "N1 NO ") {
			t.Fatalf("SELECT Missing: %q", tagged)
		}
		if _, tagged := c.command("F1", "FETCH 1 FLAGS"); strings.HasPrefix(tagged, "F1 OK ") {
			t.Fatalf("selection survived a failed SELECT: %q", tagged)
		}
	})
	t.Run("authenticated", func(t *testing.T) {
		c := selectionClient(t, newFetchMailbox())
		for _, command := range []string{"LOGIN a b", "FETCH 1 FLAGS", "CHECK", "CLOSE", "SELECT", `LIST ""`} {
			if _, tagged := c.command("R1", "%s", command); strings.HasPrefix(tagged, "R1 OK ") {
				t.Fatalf("%q: %q, want a rejection", command, tagged)
			}
			if _, tagged := c.command("L1", `LIST "" ""`); !strings.HasPrefix(tagged, "L1 OK ") {
				t.Fatalf("after %q authentication is gone: %q", command, tagged)
			}
		}
	})
	t.Run("not authenticated", func(t *testing.T) {
		c := dialMailbox(t, newFetchMailbox())
		c.line()
		for _, command := range []string{"LOGIN", "LOGIN a b", "AUTHENTICATE", "AUTHENTICATE UNKNOWN", "SELECT INBOX", `LIST "" *`} {
			if _, tagged := c.command("R1", "%s", command); strings.HasPrefix(tagged, "R1 OK ") {
				t.Fatalf("%q: %q, want a rejection", command, tagged)
			}
			if _, tagged := c.command("S1", "SELECT INBOX"); strings.HasPrefix(tagged, "S1 OK ") {
				t.Fatalf("after %q the session is authenticated: %q", command, tagged)
			}
		}
		if _, tagged := c.command("L1", "LOGIN %s %s", testUser, testPass); !strings.HasPrefix(tagged, "L1 OK ") {
			t.Fatalf("a later LOGIN: %q", tagged)
		}
	})
}

// sendRaw writes one raw command and returns the lines up to and including the
// first that does not start a further response: the tagged line for tag, or the
// first untagged BAD or BYE when tag is empty.
func sendRaw(t *testing.T, c *client, raw, tag string) []string {
	t.Helper()
	if _, err := io.WriteString(c.conn, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
	var lines []string
	for {
		line := c.line()
		lines = append(lines, line)
		if tag != "" && strings.HasPrefix(line, tag+" ") {
			return lines
		}
		if tag == "" && (strings.HasPrefix(line, "* BAD ") || strings.HasPrefix(line, "* BYE ")) {
			return lines
		}
	}
}

// TestCommandLineFraming covers RFC 3501 §§2.2 and 9: a command is a tag, one
// SP, a command permitted in the state, and CRLF. A bare LF or a stray CR does
// not end a line; the BAD is tagged when a tag can be read, and a line ending in
// a non-synchronizing literal marker ends the session because its octets are in
// flight. A command in the wrong state gets BAD or NO, "depending upon server
// implementation" (§3), so the test takes either and requires only that it does
// not run.
//
// checklist: IMAP-INV-ABNF-09
func TestCommandLineFraming(t *testing.T) {
	for _, tc := range []struct{ raw, tag string }{
		{"A1 NOOP\n", "A1"},
		{"A1 NOOP\r\r\n", "A1"},
		{"A1 NOOP\rX\r\n", "A1"},
		{"A1 FETCH 1 FLAGS\n", "A1"},
		{"A1 SELECT \"a\rb\"\r\n", "A1"},
		{"A1\n", ""},
		{"\n", ""},
		{"A1\r\n", ""},
		{" A1 NOOP\r\n", ""},
		{"  \r\n", ""},
		{"A1  NOOP\r\n", "A1"},
		{"A1 \r\n", "A1"},
		{"A1\tNOOP\r\n", ""},
	} {
		t.Run(fmt.Sprintf("%q", tc.raw), func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			lines := sendRaw(t, c, tc.raw, tc.tag)
			want := tc.tag + " BAD "
			if tc.tag == "" {
				want = "* BAD "
			}
			if len(lines) != 1 || !strings.HasPrefix(lines[0], want) {
				t.Fatalf("%q: %q, want one %q", tc.raw, lines, want)
			}
			requireStateKept(t, c, box, tc.raw, 0, false)
		})
	}

	t.Run("a marker with a bare LF ends the session", func(t *testing.T) {
		for _, line := range []string{"A1 SELECT {5+}\nINBOX\r\nN1 NOOP\r\n", "A1 APPEND INBOX {5+}\nhello\r\nN1 NOOP\r\n"} {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			writeLiteralWire(t, c, line)
			if got := c.line(); !strings.HasPrefix(got, "A1 BAD ") {
				t.Fatalf("%q: %q", line, got)
			}
			if got := c.line(); !strings.HasPrefix(got, "* BYE ") {
				t.Fatalf("%q: %q, want BYE", line, got)
			}
			if !c.closed() {
				t.Fatalf("%q: the octets in flight became commands", line)
			}
		}
	})
	t.Run("a literal continuation ends in CRLF", func(t *testing.T) {
		box := newFetchMailbox()
		c := fetchClient(t, box, "SELECT")
		writeLiteralWire(t, c, "A1 SELECT {5}\r\n")
		if got := c.line(); !strings.HasPrefix(got, "+ ") {
			t.Fatalf("continuation: %q", got)
		}
		if _, err := io.WriteString(c.conn, "INBOX\n"); err != nil {
			t.Fatal(err)
		}
		if got := c.line(); !strings.HasPrefix(got, "A1 BAD ") {
			t.Fatalf("bare LF after a literal: %q", got)
		}
		requireStateKept(t, c, box, "bare LF after a literal", 0, false)
	})
	t.Run("state", func(t *testing.T) {
		notAuthenticated := dialMailbox(t, newFetchMailbox())
		notAuthenticated.line()
		for _, command := range []string{
			"SELECT INBOX", "EXAMINE INBOX", "CREATE a", "DELETE a", "RENAME a b", "SUBSCRIBE a", "UNSUBSCRIBE a",
			`LIST "" *`, `LSUB "" *`, "STATUS INBOX (MESSAGES)", "CHECK", "CLOSE", "EXPUNGE", "SEARCH ALL",
			"FETCH 1 FLAGS", `STORE 1 +FLAGS (\Seen)`, "COPY 1 INBOX", "UID FETCH 1 FLAGS",
		} {
			_, tagged := notAuthenticated.command("X1", "%s", command)
			if !strings.HasPrefix(tagged, "X1 BAD ") && !strings.HasPrefix(tagged, "X1 NO ") {
				t.Errorf("not authenticated: %q: %q", command, tagged)
			}
		}
		authenticated := selectionClient(t, newFetchMailbox())
		for _, command := range []string{"LOGIN a b", "AUTHENTICATE PLAIN", "CHECK", "CLOSE", "EXPUNGE", "SEARCH ALL", "FETCH 1 FLAGS", `STORE 1 +FLAGS (\Seen)`, "COPY 1 INBOX", "UID FETCH 1 FLAGS"} {
			_, tagged := authenticated.command("X1", "%s", command)
			if !strings.HasPrefix(tagged, "X1 BAD ") && !strings.HasPrefix(tagged, "X1 NO ") {
				t.Errorf("authenticated: %q: %q", command, tagged)
			}
		}
		for _, c := range []*client{notAuthenticated, authenticated} {
			for _, command := range []string{"CAPABILITY", "NOOP"} {
				if _, tagged := c.command("Y1", "%s", command); !strings.HasPrefix(tagged, "Y1 OK ") {
					t.Errorf("%q in any state: %q", command, tagged)
				}
			}
			if _, tagged := c.command("Y2", "LOGOUT"); !strings.HasPrefix(tagged, "Y2 OK ") {
				t.Errorf("LOGOUT in any state: %q", tagged)
			}
		}
	})
}

// TestAtomAndAstringPositions covers RFC 3501 §§4.1 and 9: an atom holds one or
// more ATOM-CHARs, and an astring may also be a quoted string or a literal and
// admits "]" in its bare form. An empty bare token is no astring at all.
//
// checklist: IMAP-INV-ABNF-02
func TestAtomAndAstringPositions(t *testing.T) {
	for _, atom := range []string{"a(b", "a)b", "a{b", "a%b", "a*b", `a"b`, `a\b`, "a]b", "a\x01b", "a\x7fb"} {
		for _, form := range []string{"STORE 1 +FLAGS (%s)", "STORE 1 +FLAGS %s", "SEARCH KEYWORD %s", "SEARCH UNKEYWORD %s"} {
			command := fmt.Sprintf(form, atom)
			t.Run(fmt.Sprintf("atom %q", command), func(t *testing.T) {
				box := newFetchMailbox()
				c := fetchClient(t, box, "SELECT")
				if _, tagged := c.command("B1", "%s", command); !strings.HasPrefix(tagged, "B1 BAD ") {
					t.Fatalf("%q: %q, want BAD", command, tagged)
				}
				requireStateKept(t, c, box, command, 0, false)
			})
		}
		for _, form := range []string{`LOGIN %s x`, `LOGIN x %s`, `AUTHENTICATE %s`} {
			if strings.Contains(atom, "]") && strings.HasPrefix(form, "LOGIN") {
				continue // "]" is an ASTRING-CHAR
			}
			command := fmt.Sprintf(form, atom)
			t.Run(fmt.Sprintf("astring %q", command), func(t *testing.T) {
				c := dialMailbox(t, newFetchMailbox())
				c.line()
				if _, tagged := c.command("B1", "%s", command); !strings.HasPrefix(tagged, "B1 BAD ") {
					t.Fatalf("%q: %q, want BAD", command, tagged)
				}
				if c.backend.calls != 0 {
					t.Fatalf("%q reached the backend", command)
				}
			})
		}
	}
	t.Run(`"]" is an ASTRING-CHAR`, func(t *testing.T) {
		c := dialMailbox(t, newFetchMailbox())
		c.line()
		if _, tagged := c.command("L1", "LOGIN a]b c]d"); !strings.HasPrefix(tagged, "L1 NO ") || c.backend.calls != 1 {
			t.Fatalf(`LOGIN with "]": %q, %d backend calls; want the credential checked`, tagged, c.backend.calls)
		}
		c.login()
		if _, tagged := c.command("S1", "SELECT INBOX]"); !strings.HasPrefix(tagged, "S1 NO ") {
			t.Fatalf(`SELECT INBOX]: %q, want the name looked up`, tagged)
		}
	})
	t.Run("the three astring forms name one mailbox", func(t *testing.T) {
		for _, raw := range []string{"S1 SELECT INBOX\r\n", "S1 SELECT \"INBOX\"\r\n", "S1 SELECT {5+}\r\nINBOX\r\n"} {
			c := selectionClient(t, newFetchMailbox())
			lines := sendRaw(t, c, raw, "S1")
			if got := lines[len(lines)-1]; !strings.HasPrefix(got, "S1 OK [READ-WRITE] ") {
				t.Fatalf("%q: %q", raw, got)
			}
		}
	})
	t.Run("an empty string is an astring, an empty token is not", func(t *testing.T) {
		c := dialMailbox(t, newFetchMailbox())
		c.line()
		if _, tagged := c.command("L1", `LOGIN "" ""`); !strings.HasPrefix(tagged, "L1 NO ") {
			t.Fatalf(`LOGIN "" "": %q, want the credential refused`, tagged)
		}
		for _, command := range []string{"LOGIN  x", "LOGIN x ", "LOGIN  "} {
			if _, tagged := c.command("L2", "%s", command); !strings.HasPrefix(tagged, "L2 BAD ") {
				t.Fatalf("%q: %q, want BAD", command, tagged)
			}
		}
	})
}

// TestAuthenticateGrammar covers RFC 3501 §9's authenticate, auth-type and
// base64 productions. The mechanism is an atom, so a quoted or otherwise
// malformed name is BAD while an unknown atom is an unsupported mechanism (NO).
// A response must be complete base64 groups with only the final padding forms,
// its alphabet is case-sensitive, and each is one line. RFC 3501 does not say
// which status refuses a response that does not decode: the engine answers NO,
// as IMAP-SASLIR-04 records, and this test requires only the refusal.
//
// checklist: IMAP-INV-ABNF-03
func TestAuthenticateGrammar(t *testing.T) {
	valid := plainResponse("alice", "secret")
	for _, name := range []string{`"PLAIN"`, " PLAIN", "PLA]N", "PL%AIN", "PL*AIN", "(PLAIN)"} {
		t.Run("mechanism "+name, func(t *testing.T) {
			b := &acceptingCredentials{}
			c := securityClient(t, b, imap.NopLimiter{})
			lines, tagged := c.command("A1", "AUTHENTICATE %s", name)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "A1 BAD ") || b.calls != 0 {
				t.Fatalf("%q: %q, %d backend calls", name, tagged, b.calls)
			}
		})
	}
	t.Run("a literal cannot name the mechanism", func(t *testing.T) {
		b := &acceptingCredentials{}
		c := securityClient(t, b, imap.NopLimiter{})
		lines, tagged := c.command("A1", "AUTHENTICATE {5}")
		requireExactly(t, lines)
		if !strings.HasPrefix(tagged, "A1 BAD ") {
			t.Fatalf("%q, want BAD with no continuation", tagged)
		}
	})
	t.Run("an unknown atom is an unsupported mechanism", func(t *testing.T) {
		c := securityClient(t, &acceptingCredentials{}, imap.NopLimiter{})
		if _, tagged := c.command("A1", "AUTHENTICATE GSSAPI"); tagged != "A1 NO Unsupported mechanism" {
			t.Fatalf("%q", tagged)
		}
	})
	t.Run("the mechanism name is case-insensitive", func(t *testing.T) {
		c := securityClient(t, &acceptingCredentials{}, imap.NopLimiter{})
		if got := c.authenticate("A1", "pLaIn", valid); !strings.HasPrefix(got, "A1 OK ") {
			t.Fatalf("%q", got)
		}
	})
	swapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		}
		return r
	}, valid)
	for name, response := range map[string]string{
		"case-swapped":     swapped,
		"incomplete group": valid[:len(valid)-1],
		"missing padding":  strings.TrimRight(valid, "="),
		"one char group":   "A",
		"interior padding": valid[:4] + "=" + valid[4:],
		"only padding":     "====",
		"three of padding": "A===",
		"space":            valid[:4] + " " + valid[4:],
		"outside alphabet": valid[:4] + "-" + valid[5:],
	} {
		t.Run("response "+name, func(t *testing.T) {
			b := &acceptingCredentials{}
			c := securityClient(t, b, imap.NopLimiter{})
			got := c.authenticate("A1", "PLAIN", response)
			if !strings.HasPrefix(got, "A1 NO ") && !strings.HasPrefix(got, "A1 BAD ") {
				t.Fatalf("%q: %q", response, got)
			}
			if b.calls != 0 {
				t.Fatalf("%q reached the backend: %q", response, b.user)
			}
		})
	}
	t.Run("a response is one line, and the padding forms are taken", func(t *testing.T) {
		for _, tc := range []struct{ user, pass string }{{"alice", "secret"}, {"alice", "secre"}, {"alice", "secr"}} {
			response := plainResponse(tc.user, tc.pass)
			b := &acceptingCredentials{}
			c := securityClient(t, b, imap.NopLimiter{})
			if got := c.authenticate("A1", "PLAIN", response); !strings.HasPrefix(got, "A1 OK ") || b.pass != tc.pass {
				t.Fatalf("%q: %q, password %q", response, got, b.pass)
			}
		}
	})
}

// TestLiteralFraming covers RFC 3501 §§4.3 and 9: a literal's count is an
// unsigned 32-bit number, and it counts exact octets that exclude NUL and may
// hold CR and LF, including none at all.
//
// checklist: IMAP-INV-ABNF-08
func TestLiteralFraming(t *testing.T) {
	for _, marker := range []string{"{-1}", "{4294967296}", "{99999999999999999999}", "{}", "{a}", "{ 1}", "{1 }", "{+}", "{1++}", "{0x1}", "{1"} {
		t.Run("count "+marker, func(t *testing.T) {
			for _, form := range []string{"SELECT %s", "APPEND INBOX %s"} {
				c := selectionClient(t, &appendMailbox{})
				lines, tagged := c.command("B1", form, marker)
				requireExactly(t, lines)
				if !strings.HasPrefix(tagged, "B1 BAD ") && !strings.HasPrefix(tagged, "B1 NO ") {
					t.Fatalf(form+": %q, want a refusal with no continuation", marker, tagged)
				}
			}
		})
	}
	t.Run("the largest count is refused before its octets", func(t *testing.T) {
		for _, form := range []string{"SELECT {4294967295}", "APPEND INBOX {4294967295}"} {
			c := selectionClient(t, &appendMailbox{})
			lines, tagged := c.command("B1", "%s", form)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "B1 ") || strings.HasPrefix(tagged, "B1 OK ") {
				t.Fatalf("%q: %q", form, tagged)
			}
		}
	})
	t.Run("zero octets", func(t *testing.T) {
		c := dialMailbox(t, newFetchMailbox())
		c.line()
		lines := sendRaw(t, c, "L1 LOGIN {17+}\r\n"+testUser+" {0+}\r\n\r\n", "L1")
		if got := lines[len(lines)-1]; !strings.HasPrefix(got, "L1 NO ") {
			t.Fatalf("empty password literal: %q, want the credential refused", got)
		}
		box := &appendMailbox{}
		a := selectionClient(t, box)
		lines = sendRaw(t, a, "A1 APPEND INBOX {0+}\r\n\r\n", "A1")
		if got := lines[len(lines)-1]; !strings.HasPrefix(got, "A1 OK ") || len(box.records) != 1 || box.records[0].raw != "" {
			t.Fatalf("empty message: %q, %+v", got, box.records)
		}
	})
	t.Run("octets are counted exactly, CR and LF among them", func(t *testing.T) {
		box := &appendMailbox{}
		c := selectionClient(t, box)
		lines := sendRaw(t, c, "A1 APPEND INBOX {9+}\r\nA1 BAD\r\nx\r\n", "A1")
		if got := lines[len(lines)-1]; !strings.HasPrefix(got, "A1 OK ") || len(box.records) != 1 || box.records[0].raw != "A1 BAD\r\nx" {
			t.Fatalf("%q, %+v", got, box.records)
		}
		fetch := newFetchMailbox()
		f := fetchClient(t, fetch, "SELECT")
		lines = sendRaw(t, f, "S1 SEARCH SUBJECT {4+}\r\na\r\nb\r\n", "S1")
		if got := lines[len(lines)-1]; !strings.HasPrefix(got, "S1 OK ") {
			t.Fatalf("a literal holding CRLF: %q", got)
		}
	})
	for name, raw := range map[string]string{
		"SELECT": "B1 SELECT {6+}\r\nIN\x00BOX\r\n",
		"STATUS": "B1 STATUS {6+}\r\nIN\x00BOX (MESSAGES)\r\n",
		"COPY":   "B1 COPY 1 {6+}\r\nIN\x00BOX\r\n",
		"SEARCH": "B1 SEARCH SUBJECT {3+}\r\na\x00b\r\n",
		"CREATE": "B1 CREATE {3+}\r\na\x00b\r\n",
		"RENAME": "B1 RENAME a {3+}\r\na\x00b\r\n",
		"LIST":   "B1 LIST \"\" {3+}\r\na\x00b\r\n",
		"LSUB":   "B1 LSUB \"\" {3+}\r\na\x00b\r\n",
		"APPEND": "B1 APPEND INBOX {3+}\r\na\x00b\r\n",
	} {
		t.Run("NUL in a "+name+" literal", func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			lines := sendRaw(t, c, raw, "B1")
			if got := lines[len(lines)-1]; !strings.HasPrefix(got, "B1 BAD ") {
				t.Fatalf("%q: %q, want BAD", raw, got)
			}
			requireStateKept(t, c, box, raw, 0, false)
		})
	}
	t.Run("NUL in a LOGIN literal", func(t *testing.T) {
		c := dialMailbox(t, newFetchMailbox())
		c.line()
		lines := sendRaw(t, c, "L1 LOGIN {3+}\r\na\x00b {1+}\r\nx\r\n", "L1")
		if got := lines[len(lines)-1]; !strings.HasPrefix(got, "L1 BAD ") || c.backend.calls != 0 {
			t.Fatalf("%q, %d backend calls", got, c.backend.calls)
		}
	})
}

// renamedMailbox records which folders RENAME reads, to show how it split its
// arguments.
type renamedMailbox struct {
	mailbox
	reads []string
}

func (m *renamedMailbox) Messages(folder string) ([]imap.Message, error) {
	m.reads = append(m.reads, folder)
	return nil, nil
}

// TestCommandArgumentOrder covers RFC 3501 §9's command productions: each takes
// its arguments in one order, APPEND's optional flag list before its optional
// date and both before the literal, SEARCH one or more keys, STATUS a nonempty
// item list, and UID only COPY, FETCH, SEARCH and STORE of the base protocol
// (UID MOVE and UID EXPUNGE are the extensions' own, in IMAP-MOVE and
// IMAP-UIDEXPUNGE rows).
//
// checklist: IMAP-INV-ABNF-10
func TestCommandArgumentOrder(t *testing.T) {
	for _, command := range []string{
		"COPY INBOX 1", "FETCH FLAGS 1", `STORE (\Seen) FLAGS 1`, `STORE FLAGS 1 (\Seen)`, "STATUS (MESSAGES) INBOX",
		"UID NOOP", "UID SELECT INBOX", "UID LIST", "UID CHECK", "UID CLOSE", "UID NOTACOMMAND 1", "UID FLAGS 1 FETCH",
		"UID COPY INBOX 10", "UID FETCH FLAGS 10", `UID STORE (\Seen) FLAGS 10`,
		"SEARCH", "UID SEARCH", "STATUS INBOX ()", "STATUS INBOX (NOTANITEM)",
	} {
		t.Run(command, func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			if _, tagged := c.command("B1", "%s", command); !strings.HasPrefix(tagged, "B1 BAD ") {
				t.Fatalf("%q: %q, want BAD", command, tagged)
			}
			requireStateKept(t, c, box, command, 0, false)
		})
	}
	const date = `"07-Feb-1994 21:52:25 -0800"`
	for _, args := range []string{date + ` (\Seen) {3}`, `(\Seen) {3} ` + date, "{3} " + date, `(\Seen) ` + date, `(\Seen)`, date, "abc", `(\Seen) (\Draft) {3}`, date + " " + date + " {3}"} {
		t.Run("APPEND INBOX "+args, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			lines, tagged := c.command("A1", "APPEND INBOX %s", args)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "A1 BAD ") || len(box.records) != 0 {
				t.Fatalf("%q: %q, %+v", args, tagged, box.records)
			}
		})
	}
	t.Run("APPEND in order", func(t *testing.T) {
		box := &appendMailbox{}
		c := selectionClient(t, box)
		lines := sendRaw(t, c, `A1 APPEND {5+}`+"\r\n"+`INBOX (\Seen) `+date+" {3+}\r\nabc\r\n", "A1")
		if got := lines[len(lines)-1]; !strings.HasPrefix(got, "A1 OK ") || len(box.records) != 1 || box.records[0].folder != "INBOX" || box.records[0].raw != "abc" || box.records[0].flags.Seen == nil {
			t.Fatalf("%q, %+v", got, box.records)
		}
	})
	t.Run("RENAME takes two astrings of any form", func(t *testing.T) {
		for _, tc := range []struct{ raw, read string }{
			{"R1 RENAME \"a b\" c\r\n", "a b"},
			{"R1 RENAME {3+}\r\na b c\r\n", "a b"},
			{"R1 RENAME \"a b\" \"c d\"\r\n", "a b"},
		} {
			box := &renamedMailbox{}
			c := selectionClient(t, box)
			lines := sendRaw(t, c, tc.raw, "R1")
			if got := lines[len(lines)-1]; !strings.HasPrefix(got, "R1 OK ") || !slices.Contains(box.reads, tc.read) {
				t.Fatalf("%q: %q, read %q", tc.raw, got, box.reads)
			}
		}
	})
}

// TestDateGrammar covers RFC 3501 §9's date and date-time. A SEARCH date is bare
// or quoted and never a literal, which is refused before its octets are invited.
// An APPEND date-time is one quoted fixed-width string: a day of two characters,
// a four-digit year and a signed four-digit zone. A literal in that place reads
// as the message itself, so what follows it breaks the framing.
//
// checklist: IMAP-INV-ABNF-12
func TestDateGrammar(t *testing.T) {
	for _, key := range []string{"BEFORE", "ON", "SINCE", "SENTBEFORE", "SENTON", "SENTSINCE"} {
		t.Run("SEARCH "+key, func(t *testing.T) {
			c := fetchClient(t, newFetchMailbox(), "SELECT")
			for _, date := range []string{"1-Feb-1994", "01-Feb-1994", `"1-Feb-1994"`, `"01-Feb-1994"`, "1-feb-1994"} {
				if _, tagged := c.command("S1", "SEARCH %s %s", key, date); !strings.HasPrefix(tagged, "S1 OK ") {
					t.Errorf("%s %s: %q", key, date, tagged)
				}
			}
			for _, date := range []string{"1-Feb-94", "001-Feb-1994", "32-Feb-1994", "1-Foo-1994", "1 Feb 1994", `"1-Feb-1994`, "1994-02-01", `"1-Feb-1994 10:00:00 +0000"`} {
				if _, tagged := c.command("S2", "SEARCH %s %s", key, date); !strings.HasPrefix(tagged, "S2 BAD ") {
					t.Errorf("%s %s: %q, want BAD", key, date, tagged)
				}
			}
			lines, tagged := c.command("S3", "SEARCH %s {10}", key)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "S3 BAD ") {
				t.Fatalf("%s literal: %q, want BAD with no continuation", key, tagged)
			}
		})
	}
	for _, date := range []string{`"07-Feb-1994 21:52:25 -0800"`, `" 7-Feb-1994 21:52:25 -0800"`, `"07-feb-1994 21:52:25 +0000"`} {
		t.Run("APPEND "+date, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			lines := sendRaw(t, c, "A1 APPEND INBOX "+date+" {3+}\r\nabc\r\n", "A1")
			if got := lines[len(lines)-1]; !strings.HasPrefix(got, "A1 OK ") || len(box.records) != 1 {
				t.Fatalf("%s: %q", date, got)
			}
		})
	}
	for _, date := range []string{
		`"7-Feb-1994 21:52:25 -0800"`, `"07-Feb-94 21:52:25 -0800"`, `"07-Feb-1994 21:52:25 0800"`,
		`"07-Feb-1994 21:52 -0800"`, `"07-Feb-1994 21:52:25"`, `"07-Foo-1994 21:52:25 -0800"`,
		`"32-Feb-1994 21:52:25 -0800"`, `"07-Feb-1994 25:52:25 -0800"`, `"07-Feb-1994 21:52:25 -08"`,
		`07-Feb-1994`,
	} {
		t.Run("APPEND refuses "+date, func(t *testing.T) {
			box := &appendMailbox{}
			c := selectionClient(t, box)
			lines, tagged := c.command("A1", "APPEND INBOX %s {3}", date)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "A1 BAD ") || len(box.records) != 0 {
				t.Fatalf("%s: %q", date, tagged)
			}
		})
	}
	t.Run("APPEND reads a literal date as the message", func(t *testing.T) {
		box := &appendMailbox{}
		c := selectionClient(t, box)
		lines := sendRaw(t, c, "A1 APPEND INBOX {26+}\r\n07-Feb-1994 21:52:25 -0800 {3+}\r\nabc\r\n", "A1")
		if got := lines[len(lines)-1]; !strings.HasPrefix(got, "A1 BAD ") || len(box.records) != 0 {
			t.Fatalf("%q, %+v", got, box.records)
		}
		if got := c.line(); !strings.HasPrefix(got, "* BYE ") {
			t.Fatalf("after the broken framing: %q, want BYE", got)
		}
		if !c.closed() {
			t.Fatal("the stream was reused")
		}
	})
}

// encapsulatedRaw has a text part and a message/rfc822 part.
const encapsulatedRaw = "Subject: outer\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
	"--b\r\nContent-Type: text/plain\r\nX-Part: one\r\n\r\nfirst\r\n" +
	"--b\r\nContent-Type: message/rfc822\r\n\r\nSubject: inner\r\n\r\nsecond\r\n--b--\r\n"

// TestFetchGrammar covers RFC 3501 §9's fetch, section and partial productions.
// Sections take positive numbers, HEADER and TEXT at the top or under an
// encapsulated message, MIME only after a number, and a nonempty header list of
// astrings. A partial has an origin and a nonzero count. What a HEADER or TEXT
// section of a part that is not a message answers is left to the server: the
// engine sends an empty section, and this test requires only that no octets of
// another section come back.
//
// checklist: IMAP-INV-ABNF-15
func TestFetchGrammar(t *testing.T) {
	for _, item := range []string{
		"()", "(ALL)", "(FAST)", "(FULL)", "(ALL FLAGS)", "(FAST FLAGS)", "ALL FLAGS", "FLAGS UID", "NOTANITEM", "(FLAGS NOTANITEM)",
		"BODY[0]", "BODY[1.0]", "BODY[01]", "BODY[1.]", "BODY[.1]", "BODY[MIME]", "BODY[TEXT.MIME]", "BODY[HEADER.MIME]",
		"BODY[HEADER.FIELDS]", "BODY[HEADER.FIELDS ()]", "BODY[HEADER.FIELDS.NOT]", "BODY[HEADER.FIELDS.NOT ()]", "BODY[HEADER.FIELDS (a  b)]", "BODY[HEADER.FIELDS ( a)]",
		"BODY[1.HEADER.FIELDS ()]", "BODY[NOTASECTION]", "BODY[1", "BODY.PEEK", "BODY.PEEK[1", "RFC822.NOTANITEM",
		"BODY[]<0.0>", "BODY[]<0>", "BODY[]<>", "BODY[]<a.1>", "BODY[]<1.a>", "BODY[]<1.>", "BODY[]<.1>", "BODY[]<4294967296.1>", "BODY[]<0.4294967296>", "BODY[]<-1.1>", "BODY[]<0.1",
	} {
		t.Run("refuses "+item, func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			if _, tagged := c.command("B1", "FETCH 1 %s", item); !strings.HasPrefix(tagged, "B1 BAD ") {
				t.Fatalf("%q: %q, want BAD", item, tagged)
			}
			requireStateKept(t, c, box, item, 0, false)
		})
	}
	for _, tc := range []struct {
		item, literal string
	}{
		{"BODY.PEEK[TEXT]", "--b\r\nContent-Type: text/plain\r\nX-Part: one\r\n\r\nfirst\r\n--b\r\nContent-Type: message/rfc822\r\n\r\nSubject: inner\r\n\r\nsecond\r\n--b--\r\n"},
		{"BODY.PEEK[1]", "first"},
		{"BODY.PEEK[1.MIME]", "Content-Type: text/plain\r\nX-Part: one\r\n\r\n"},
		{"BODY.PEEK[2.MIME]", "Content-Type: message/rfc822\r\n\r\n"},
		{"BODY.PEEK[2.HEADER]", "Subject: inner\r\n\r\n"},
		{"BODY.PEEK[2.TEXT]", "second"},
		{"BODY.PEEK[2.HEADER.FIELDS (Subject)]", "Subject: inner\r\n\r\n"},
		{`BODY.PEEK[HEADER.FIELDS ("Subject")]`, "Subject: outer\r\n\r\n"},
		{`BODY.PEEK[HEADER.FIELDS (X-None "Subject")]`, "Subject: outer\r\n\r\n"},
		{"BODY.PEEK[HEADER.FIELDS.NOT (Content-Type)]", "Subject: outer\r\n\r\n"},
		{"BODY.PEEK[1]<1.2>", "ir"},
		{"BODY.PEEK[1]<0.1>", "f"},
		{"BODY.PEEK[1]<4294967295.1>", ""},
		{"BODY.PEEK[1.HEADER]", ""},
		{"BODY.PEEK[1.TEXT]", ""},
	} {
		t.Run("takes "+tc.item, func(t *testing.T) {
			box := newFetchMailbox()
			box.raw[10] = encapsulatedRaw
			c := fetchClient(t, box, "SELECT")
			replies, tagged := fetchCommand(t, c, "FETCH 1 "+tc.item)
			requireOK(t, tagged, "F1")
			if len(replies) != 1 || len(replies[0].literals) != 1 || replies[0].literals[0] != tc.literal {
				t.Fatalf("%s: %#v, want literal %q", tc.item, replies, tc.literal)
			}
		})
	}
	t.Run("the macros stand alone", func(t *testing.T) {
		for _, macro := range []string{"ALL", "FAST", "FULL", "all"} {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			_, tagged := c.command("F2", "FETCH 1 %s", macro)
			requireOK(t, tagged, "F2")
		}
	})
	t.Run("a header list takes a literal astring", func(t *testing.T) {
		box := newFetchMailbox()
		box.raw[10] = encapsulatedRaw
		c := fetchClient(t, box, "SELECT")
		if _, err := io.WriteString(c.conn, "F1 FETCH 1 BODY.PEEK[HEADER.FIELDS ({7+}\r\nSubject)]\r\n"); err != nil {
			t.Fatal(err)
		}
		line := c.line()
		if !strings.Contains(line, "BODY[HEADER.FIELDS (") || !strings.HasSuffix(line, "{18}") {
			t.Fatalf("%q", line)
		}
	})
}

// TestFlagGrammar covers RFC 3501 §§2.3.2 and 9's flag productions. STORE and
// APPEND never take \Recent or \*, in any case, and refuse them before anything
// is written or invited. Only PERMANENTFLAGS offers \*, FLAGS and FETCH name
// system flags only, and a flag extension a client names is never generated back
// or stored.
//
// checklist: IMAP-INV-ABNF-16
func TestFlagGrammar(t *testing.T) {
	for _, flag := range []string{`\Recent`, `\RECENT`, `\recent`, `\*`} {
		t.Run("STORE "+flag, func(t *testing.T) {
			for _, form := range []string{"STORE 1 +FLAGS (%s)", "STORE 1 FLAGS %s", "UID STORE 10 -FLAGS.SILENT (%s)", `STORE 1 +FLAGS (\Seen %s)`} {
				box := newFetchMailbox()
				c := fetchClient(t, box, "SELECT")
				if _, tagged := c.command("B1", form, flag); !strings.HasPrefix(tagged, "B1 BAD ") {
					t.Fatalf(form+": %q, want BAD", flag, tagged)
				}
				requireStateKept(t, c, box, flag, 0, false)
			}
		})
		t.Run("APPEND "+flag, func(t *testing.T) {
			for _, list := range []string{"(%s)", `(\Seen %s)`} {
				box := &appendMailbox{}
				c := selectionClient(t, box)
				lines, tagged := c.command("B1", "APPEND INBOX "+list+" {3}", flag)
				requireExactly(t, lines)
				if !strings.HasPrefix(tagged, "B1 BAD ") || len(box.records) != 0 {
					t.Fatalf(list+": %q, want BAD with no continuation", flag, tagged)
				}
			}
		})
	}
	t.Run("only PERMANENTFLAGS offers the wildcard", func(t *testing.T) {
		box := newFetchMailbox()
		c := selectionClient(t, box)
		lines, tagged := c.command("S1", "SELECT INBOX")
		requireOK(t, tagged, "S1")
		var flags, permanent string
		for _, line := range lines {
			if rest, ok := strings.CutPrefix(line, "* FLAGS "); ok {
				flags = rest
			}
			if strings.HasPrefix(line, "* OK [PERMANENTFLAGS ") {
				permanent = line
			}
		}
		if flags == "" || strings.Contains(flags, `\*`) || strings.Contains(strings.ToLower(flags), `\recent`) {
			t.Fatalf("FLAGS: %q", flags)
		}
		if !strings.Contains(permanent, `\*`) || strings.Contains(strings.ToLower(permanent), `\recent`) {
			t.Fatalf("PERMANENTFLAGS: %q", permanent)
		}
		for _, command := range []string{"FETCH 1 FLAGS", `STORE 1 +FLAGS (\Seen)`, "FETCH 1 (FLAGS UID)"} {
			lines, tagged := c.command("F1", "%s", command)
			requireOK(t, tagged, "F1")
			for _, line := range lines {
				if strings.Contains(line, `\*`) {
					t.Fatalf("%s: %q", command, line)
				}
			}
		}
	})
	t.Run("a flag extension is neither stored nor generated", func(t *testing.T) {
		box := newFetchMailbox()
		c := fetchClient(t, box, "SELECT")
		lines, tagged := c.command("S1", `STORE 1 +FLAGS (\Extension)`)
		requireOK(t, tagged, "S1")
		requireExactly(t, lines, `* 1 FETCH (FLAGS ())`)
		lines, tagged = c.command("F1", "FETCH 1 FLAGS")
		requireOK(t, tagged, "F1")
		if len(lines) != 1 || strings.Contains(lines[0], "Extension") {
			t.Fatalf("FETCH generated the flag: %q", lines)
		}
		for _, update := range box.updates[10] {
			if slices.Contains(update.Keywords, `\Extension`) || slices.Contains(update.Keywords, "Extension") {
				t.Fatalf("the backend was given the flag: %+v", update)
			}
		}
	})
}

// TestStringGrammar covers RFC 3501 §§4.3 and 9. A quoted string holds no CR or
// LF and escapes only DQUOTE and backslash; anything else is BAD. NIL and "" are
// different on the wire: a header that is absent answers NIL, one that is
// present and empty answers "". A value a quoted string cannot carry is sent as
// a literal, and one it can is escaped.
//
// checklist: IMAP-INV-ABNF-19
func TestStringGrammar(t *testing.T) {
	for _, argument := range []string{`"a\b"`, `"a\`, `"a`, `a"`, `"a"b`, `"a""b"`, `"a\nb"`, "\"a\rb\"", `"\x"`} {
		t.Run("refuses "+argument, func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			if _, tagged := c.command("B1", "SELECT %s", argument); !strings.HasPrefix(tagged, "B1 BAD ") {
				t.Fatalf("%q: %q, want BAD", argument, tagged)
			}
			requireStateKept(t, c, box, argument, 0, false)
		})
	}
	t.Run("the two escapes", func(t *testing.T) {
		box := &renamedMailbox{}
		c := selectionClient(t, box)
		if _, tagged := c.command("R1", `RENAME "a\"b\\c" d`); !strings.HasPrefix(tagged, "R1 OK ") || !slices.Contains(box.reads, `a"b\c`) {
			t.Fatalf("%q, read %q", tagged, box.reads)
		}
	})
	t.Run("NIL as an argument is an atom", func(t *testing.T) {
		box := newFetchMailbox()
		c := dialMailbox(t, box)
		c.line()
		if _, tagged := c.command("L1", "LOGIN NIL NIL"); !strings.HasPrefix(tagged, "L1 NO ") || c.backend.calls != 1 {
			t.Fatalf("%q, %d backend calls", tagged, c.backend.calls)
		}
		c.login()
		if _, tagged := c.command("S1", "SELECT NIL"); !strings.HasPrefix(tagged, "S1 NO ") {
			t.Fatalf(`SELECT NIL: %q, want a mailbox named "NIL" looked up`, tagged)
		}
	})
	t.Run("NIL and the empty string differ in a response", func(t *testing.T) {
		for _, tc := range []struct{ raw, want string }{
			{"From: a@example.com\r\n\r\nx\r\n", `ENVELOPE (NIL NIL `},
			{"Date: \r\nSubject: \r\nFrom: a@example.com\r\n\r\nx\r\n", `ENVELOPE ("" "" `},
		} {
			box := newFetchMailbox()
			box.raw[10] = tc.raw
			c := fetchClient(t, box, "SELECT")
			lines, tagged := c.command("F1", "FETCH 1 ENVELOPE")
			requireOK(t, tagged, "F1")
			if len(lines) != 1 || !strings.Contains(lines[0], tc.want) {
				t.Fatalf("%q: %q, want %q", tc.raw, lines, tc.want)
			}
		}
	})
	t.Run("a response escapes what a quoted string may hold", func(t *testing.T) {
		box := newFetchMailbox()
		box.raw[10] = "Subject: a\"b\\c\r\n\r\nx\r\n"
		c := fetchClient(t, box, "SELECT")
		lines, tagged := c.command("F1", "FETCH 1 ENVELOPE")
		requireOK(t, tagged, "F1")
		if len(lines) != 1 || !strings.Contains(lines[0], `"a\"b\\c"`) {
			t.Fatalf("%q", lines)
		}
	})
	t.Run("a mailbox name holding a quoted-special is escaped", func(t *testing.T) {
		c := selectionClient(t, specialNameMailbox{})
		lines, tagged := c.command("L1", `LIST "" *`)
		requireOK(t, tagged, "L1")
		requireExactly(t, lines, `* LIST () "/" "a\"b\\c"`)
	})
	t.Run("a value a quoted string cannot carry is a literal", func(t *testing.T) {
		box := newFetchMailbox()
		box.raw[10] = "Subject: caf\xc3\xa9\r\n\r\nx\r\n"
		c := utf8FetchClient(t, box)
		replies, tagged := fetchCommand(t, c, "FETCH 1 ENVELOPE")
		requireOK(t, tagged, "F1")
		if len(replies) != 1 || !strings.Contains(replies[0].framing, "ENVELOPE (NIL {5} NIL") || !slices.Equal(replies[0].literals, []string{"caf\xc3\xa9"}) {
			t.Fatalf("%#v", replies)
		}
	})
}

// specialNameMailbox has one folder whose name holds both quoted-specials.
type specialNameMailbox struct{ mailbox }

func (specialNameMailbox) Folders() ([]imap.Folder, error) {
	return []imap.Folder{{Name: `a"b\c`}}, nil
}

// statusLine is RFC 3501 §9's resp-cond-state, resp-cond-bye and greeting: text
// follows an optional code, and text is one or more TEXT-CHARs.
var statusLine = regexp.MustCompile(`^(\*|[^ ]+) (OK|NO|BAD|BYE) (\[([^\]]+)\] )?[\x20-\x7e]+$`)

// numericCode is a response code whose argument is RFC 3501 §9's nz-number.
var numericCode = regexp.MustCompile(`^(UIDNEXT|UIDVALIDITY|UNSEEN) ([0-9]+)$`)

func requireResponseText(t *testing.T, line string) {
	t.Helper()
	m := statusLine.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("%q is no resp-text", line)
	}
	if n := numericCode.FindStringSubmatch(m[4]); n != nil {
		v, err := strconv.ParseUint(n[2], 10, 32)
		if err != nil || v == 0 || n[2][0] == '0' {
			t.Fatalf("%q: %s is not an nz-number", line, n[1])
		}
	}
}

// failingStore refuses every flag write with text that does not fit a response.
type failingStore struct{ *fetchMailbox }

func (failingStore) Store(uint32, imap.FlagUpdate) error {
	return errors.New("private diagnostic\r\n* BYE forged \xff")
}

// TestResponseTextGrammar covers RFC 3501 §9's tag, resp-text and resp-text-code.
// Every status response has one or more 7-bit TEXT-CHARs after any code, a code's
// number is positive, and neither a client's literal octets nor a host's error
// text reach the line, where they could add a response or break its grammar.
//
// checklist: IMAP-INV-ABNF-20
func TestResponseTextGrammar(t *testing.T) {
	t.Run("the status lines of a session", func(t *testing.T) {
		box := newFetchMailbox()
		c := dialMailbox(t, box)
		requireResponseText(t, c.line())
		var transcript []string
		run := func(tag, format string, args ...any) {
			t.Helper()
			lines, tagged := c.command(tag, format, args...)
			transcript = append(append(transcript, lines...), tagged)
		}
		run("a1", "CAPABILITY")
		run("a2", "NOTACOMMAND")
		run("a3", "LOGIN")
		run("a4", "LOGIN wrong wrong")
		run("a]5", "LOGIN %s %s", testUser, testPass)
		run("a6", "SELECT INBOX")
		run("a7", "EXAMINE INBOX")
		run("a8", "SELECT INBOX")
		run("a9", "STATUS INBOX (MESSAGES UIDNEXT UIDVALIDITY UNSEEN)")
		run("a10", "SEARCH NOT")
		run("a11", "SEARCH CHARSET NOPE ALL")
		run("a12", `STORE 1 +FLAGS (\Recent)`)
		run("a13", "FETCH 1 FLAGS")
		run("a14", `COPY 1 "a&b"`)
		run("a15", "SELECT Missing")
		for _, line := range transcript {
			if strings.HasPrefix(line, "a") || strings.HasPrefix(line, "* OK") || strings.HasPrefix(line, "* NO") || strings.HasPrefix(line, "* BAD") {
				requireResponseText(t, line)
			}
		}
	})
	for name, raw := range map[string]string{
		"STATUS item":      "B1 STATUS INBOX ({9+}\r\nX\r\n* BYE é)\r\n",
		"LIST selection":   "B1 LIST ({9+}\r\nX\r\n* BYE é) \"\" *\r\n",
		"LIST return":      "B1 LIST \"\" * RETURN ({9+}\r\nX\r\n* BYE é)\r\n",
		"SEARCH charset":   "B1 SEARCH CHARSET {9+}\r\nX\r\n* BYE é ALL\r\n",
		"mailbox name":     "B1 SELECT {9+}\r\nX\r\n* BYE é\r\n",
		"STATUS mailbox":   "B1 STATUS {9+}\r\nX\r\n* BYE é (MESSAGES)\r\n",
		"RENAME from name": "B1 RENAME INBOX {9+}\r\nX\r\n* BYE é\r\n",
	} {
		t.Run("literal octets in the "+name, func(t *testing.T) {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			lines := sendRaw(t, c, raw, "B1")
			if len(lines) != 1 {
				t.Fatalf("%q: %q, want one line", raw, lines)
			}
			requireResponseText(t, lines[0])
			if !strings.HasPrefix(lines[0], "B1 BAD ") && !strings.HasPrefix(lines[0], "B1 NO ") {
				t.Fatalf("%q: %q, want a refusal", raw, lines[0])
			}
			if strings.Contains(lines[0], "BYE") {
				t.Fatalf("%q: %q echoes the literal", raw, lines[0])
			}
		})
	}
	t.Run("a host error stays out of the text", func(t *testing.T) {
		for _, command := range []string{`STORE 1 +FLAGS (\Seen)`, `UID STORE 10 +FLAGS (\Seen)`} {
			c := selectionClient(t, failingStore{newFetchMailbox()})
			_, tagged := c.command("S1", "SELECT INBOX")
			requireOK(t, tagged, "S1")
			lines, tagged := c.command("N1", "%s", command)
			requireExactly(t, lines)
			requireResponseText(t, tagged)
			if !strings.HasPrefix(tagged, "N1 NO ") || strings.Contains(tagged, "private") {
				t.Fatalf("%s: %q", command, tagged)
			}
		}
	})
	t.Run("the tag comes back as sent", func(t *testing.T) {
		c := dial(t)
		c.line()
		for _, tag := range []string{"a", "A1", "a.b-c_d", "a]b", "~!@#$^&='|;:,?/<>`"} {
			lines := sendRaw(t, c, tag+" NOOP\r\n", tag)
			if got := lines[len(lines)-1]; got != tag+" OK NOOP completed" {
				t.Fatalf("tag %q: %q", tag, got)
			}
		}
		for _, tag := range []string{"a+b", "+", "*", "a(b", "a%b", `a"b`, `a\b`, "a{b"} {
			lines := sendRaw(t, c, tag+" NOOP\r\n", "")
			if len(lines) != 1 || !strings.HasPrefix(lines[0], "* BAD ") {
				t.Fatalf("tag %q: %q, want one untagged BAD", tag, lines)
			}
		}
	})
}

// searchNumbers is the numbers of an untagged SEARCH, as text.
func searchNumbers(t *testing.T, lines []string) string {
	t.Helper()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "* SEARCH") {
		t.Fatalf("%q, want one untagged SEARCH", lines)
	}
	return strings.TrimSpace(strings.TrimPrefix(lines[0], "* SEARCH"))
}

// TestSequenceSetAndSearchGrammar covers RFC 3501 §9's search-key, seq-number,
// seq-range and sequence-set. Keys nest through NOT, OR and groups; a set's ends
// are positive numbers or *, a range is inclusive whichever end comes first, and
// overlaps may be coalesced. A UID * on an empty mailbox is its UIDNEXT, which no
// message has, so it answers nothing. A number beyond the mailbox also answers
// nothing, here and in IMAP-SYNTAX-08: §9's "should" there is not the SHOULD of
// RFC 2119, and IMAP-INV-ABNF-23 records that the engine keeps this choice.
//
// checklist: IMAP-INV-ABNF-21
func TestSequenceSetAndSearchGrammar(t *testing.T) {
	selected := func(t *testing.T) *client { return storeClient(t, newStoreMailbox()) }

	for _, set := range []string{"0", "00", "01", "1:0", "0:1", "1:", ":1", "1,", ",1", "1,,2", "1:2:3", "a", "1a", "-1", "+1", "4294967296", "1:4294967296", "1.2", "*:", ":*", "**"} {
		t.Run("refuses "+set, func(t *testing.T) {
			c := selected(t)
			for _, form := range []string{"FETCH %s FLAGS", `STORE %s +FLAGS (\Seen)`, "COPY %s INBOX", "SEARCH %s", "SEARCH UID %s", "UID FETCH %s FLAGS", `UID STORE %s +FLAGS (\Seen)`, "UID COPY %s INBOX"} {
				command := fmt.Sprintf(form, set)
				if _, tagged := c.command("B1", "%s", command); !strings.HasPrefix(tagged, "B1 BAD ") {
					t.Errorf("%q: %q, want BAD", command, tagged)
				}
			}
		})
	}
	for _, tc := range []struct{ query, want string }{
		{"1", "1"},
		{"*", "4"},
		{"2:3", "2 3"},
		{"3:2", "2 3"},
		{"*:3", "3 4"},
		{"3:*", "3 4"},
		{"1:*", "1 2 3 4"},
		{"*:*", "4"},
		{"1,3", "1 3"},
		{"3,1", "1 3"},
		{"1,1,1", "1"},
		{"1:2,2:3", "1 2 3"},
		{"4294967295", ""},
		{"UID 4001", "1"},
		{"UID *", "4"},
		{"UID 4003:*", "3 4"},
		{"UID *:4003", "3 4"},
		{"UID 4002,4001", "1 2"},
		{"NOT 1", "2 3 4"},
		{"NOT NOT 1", "1"},
		{"NOT (1 2)", "1 2 3 4"},
		{"OR 1 2", "1 2"},
		{"OR 1 OR 2 3", "1 2 3"},
		{"OR (1 SEEN) (2 SEEN)", "2"},
		{"(SEEN (FLAGGED))", "4"},
		{"((((ALL))))", "1 2 3 4"},
		{"SEEN FLAGGED", "4"},
		{"NOT SEEN", "1 3"},
		{"LARGER 0", ""},
		{"SMALLER 4294967295", "1 2 3 4"},
		{"all", "1 2 3 4"},
		{"oR sEeN fLaGgEd", "2 4"},
	} {
		t.Run("SEARCH "+tc.query, func(t *testing.T) {
			c := selected(t)
			lines, tagged := c.command("S1", "SEARCH %s", tc.query)
			requireOK(t, tagged, "S1")
			if got := searchNumbers(t, lines); got != tc.want {
				t.Fatalf("SEARCH %s: %q, want %q", tc.query, got, tc.want)
			}
		})
	}
	for _, tc := range []struct{ query, want string }{
		{"UID 4001:4002", "4001 4002"},
		{"UID 4002:4001", "4001 4002"},
		{"3", "4003"},
		{"*", "4004"},
	} {
		t.Run("UID SEARCH "+tc.query, func(t *testing.T) {
			c := selected(t)
			lines, tagged := c.command("S1", "UID SEARCH %s", tc.query)
			requireOK(t, tagged, "S1")
			if got := searchNumbers(t, lines); got != tc.want {
				t.Fatalf("UID SEARCH %s: %q, want %q", tc.query, got, tc.want)
			}
		})
	}
	for _, query := range []string{
		"NOTAKEY", "NOT", "NOT NOT", "OR", "OR 1", "(", ")", "(ALL", "ALL)", "()", "(())", "OR () ()", `"ALL"`, "UID", "UID a",
		"LARGER", "LARGER a", "LARGER -1", "LARGER 4294967296", `LARGER "1"`, "SMALLER", "SUBJECT", "FROM", "HEADER", "HEADER a", "KEYWORD", `KEYWORD "a"`, "ON", "ON a",
	} {
		t.Run("refuses SEARCH "+query, func(t *testing.T) {
			c := selected(t)
			lines, tagged := c.command("B1", "SEARCH %s", query)
			requireExactly(t, lines)
			if !strings.HasPrefix(tagged, "B1 BAD ") {
				t.Fatalf("%q: %q, want BAD", query, tagged)
			}
		})
	}
	t.Run("string operands are atoms, quoted strings or literals", func(t *testing.T) {
		box := newFetchMailbox()
		box.messages[0].Subject = "hello there"
		c := fetchClient(t, box, "SELECT")
		for _, raw := range []string{"S1 SEARCH SUBJECT hello\r\n", "S1 SEARCH SUBJECT \"hello there\"\r\n", "S1 SEARCH SUBJECT {11+}\r\nhello there\r\n"} {
			lines := sendRaw(t, c, raw, "S1")
			if len(lines) != 2 || lines[0] != "* SEARCH 1" {
				t.Fatalf("%q: %q", raw, lines)
			}
		}
	})
	t.Run("an empty mailbox", func(t *testing.T) {
		c := storeClient(t, &storeMailbox{updates: map[uint32][]imap.FlagUpdate{}})
		for _, command := range []string{"SEARCH *", "SEARCH 1:*", "SEARCH UID *", "UID SEARCH UID *", "UID SEARCH UID 1:*"} {
			lines, tagged := c.command("S1", "%s", command)
			requireOK(t, tagged, "S1")
			if got := searchNumbers(t, lines); got != "" {
				t.Fatalf("%s: %q", command, got)
			}
		}
		for _, command := range []string{"UID FETCH * UID", "UID FETCH 1:* UID", "FETCH * UID", "FETCH 1:* UID"} {
			lines, tagged := c.command("F1", "%s", command)
			requireOK(t, tagged, "F1")
			requireExactly(t, lines)
		}
	})
}

// TestGrammarOverlapPriority covers RFC 3501 §9's notes. Tokens are
// case-insensitive, SP is one space and NUL is banned, and where alternatives
// overlap the earlier wins: a backslash flag is a system flag rather than an
// extension in any case, INBOX in any case is the mailbox rather than a name
// like any other, and a MESSAGE/RFC822 or TEXT type takes its own body form
// rather than the basic one.
//
// checklist: IMAP-INV-ABNF-22
func TestGrammarOverlapPriority(t *testing.T) {
	t.Run("tokens in any case", func(t *testing.T) {
		box := newFetchMailbox()
		c := selectionClient(t, box)
		for _, tc := range []struct{ command, reply string }{
			{"nOoP", "OK"},
			{"cApAbIlItY", "OK"},
			{"sElEcT iNbOx", "OK"},
			{"FeTcH 1 FlAgS", "OK"},
			{"fetch 1 (uid flags rfc822.size)", "OK"},
			{"uId fEtCh 10 fLaGs", "OK"},
			{`StOrE 1 +fLaGs.sIlEnT (\sEeN)`, "OK"},
			{"SeArCh sEeN", "OK"},
			{"sTaTuS iNbOx (mEsSaGeS)", "OK"},
			{`list "" iNbOx`, "OK"},
		} {
			if _, tagged := c.command("C1", "%s", tc.command); !strings.HasPrefix(tagged, "C1 "+tc.reply+" ") {
				t.Errorf("%q: %q", tc.command, tagged)
			}
		}
	})
	t.Run("one SP and no NUL", func(t *testing.T) {
		for _, command := range []string{"FETCH\t1 FLAGS", "FETCH 1\tFLAGS", "FETCH  1 FLAGS", "SEARCH\tALL", "SEARCH ALL\tSEEN", "NOOP\x00", "FETCH 1 FLAGS\x00", "SELECT \"IN\x00BOX\""} {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			if _, tagged := c.command("B1", "%s", command); !strings.HasPrefix(tagged, "B1 BAD ") {
				t.Errorf("%q: %q, want BAD", command, tagged)
			}
		}
	})
	t.Run("a backslash flag is a system flag in any case", func(t *testing.T) {
		for _, flag := range []string{`\Seen`, `\SEEN`, `\seen`, `\sEeN`} {
			box := newFetchMailbox()
			c := fetchClient(t, box, "SELECT")
			lines, tagged := c.command("S1", "STORE 1 +FLAGS (%s)", flag)
			requireOK(t, tagged, "S1")
			requireExactly(t, lines, `* 1 FETCH (FLAGS (\Seen))`)
			if !box.messages[0].Seen {
				t.Fatalf("%s did not set the system flag", flag)
			}
		}
		box := newFetchMailbox()
		c := fetchClient(t, box, "SELECT")
		_, tagged := c.command("S1", "STORE 1 +FLAGS (Seen)")
		requireOK(t, tagged, "S1")
		if box.messages[0].Seen {
			t.Fatal("the keyword Seen set the system flag")
		}
	})
	t.Run("INBOX in any case is the mailbox", func(t *testing.T) {
		for _, name := range []string{"INBOX", "inbox", "iNbOx", `"InBoX"`} {
			for _, command := range []string{"SELECT %s", "EXAMINE %s", "STATUS %s (MESSAGES)"} {
				c := selectionClient(t, newFetchMailbox())
				if _, tagged := c.command("S1", command, name); !strings.HasPrefix(tagged, "S1 OK ") {
					t.Fatalf(command+": %q", name, tagged)
				}
			}
		}
		c := selectionClient(t, newFetchMailbox())
		if _, tagged := c.command("S1", "SELECT INBOXES"); !strings.HasPrefix(tagged, "S1 NO ") {
			t.Fatalf("SELECT INBOXES: %q", tagged)
		}
	})
	for _, media := range []string{"message/rfc822", "MESSAGE/RFC822", "Message/Rfc822"} {
		t.Run("a "+media+" part takes the message form", func(t *testing.T) {
			box := newFetchMailbox()
			box.raw[10] = "Content-Type: " + media + "\r\n\r\nSubject: inner\r\n\r\nbody\r\n"
			c := fetchClient(t, box, "SELECT")
			lines, tagged := c.command("F1", "FETCH 1 BODY")
			requireOK(t, tagged, "F1")
			if len(lines) != 1 || !strings.Contains(lines[0], `BODY ("MESSAGE" "RFC822" `) || !strings.Contains(lines[0], `"inner"`) {
				t.Fatalf("%q", lines)
			}
		})
	}
	for _, media := range []string{"text/plain", "TEXT/PLAIN", "Text/Html"} {
		t.Run("a "+media+" part takes the text form", func(t *testing.T) {
			box := newFetchMailbox()
			box.raw[10] = "Content-Type: " + media + "\r\n\r\nbody\r\n"
			c := fetchClient(t, box, "SELECT")
			lines, tagged := c.command("F1", "FETCH 1 BODY")
			requireOK(t, tagged, "F1")
			// The text form ends in the line count; the basic form has none.
			if len(lines) != 1 || !regexp.MustCompile(`BODY \("TEXT" "[A-Z]+" .* "7BIT" 6 1\)\)$`).MatchString(lines[0]) {
				t.Fatalf("%q", lines)
			}
		})
	}
	t.Run("another type takes the basic form", func(t *testing.T) {
		box := newFetchMailbox()
		box.raw[10] = "Content-Type: application/pdf\r\n\r\nbody\r\n"
		c := fetchClient(t, box, "SELECT")
		lines, tagged := c.command("F1", "FETCH 1 BODY")
		requireOK(t, tagged, "F1")
		if len(lines) != 1 || !regexp.MustCompile(`BODY \("APPLICATION" "PDF" .* "7BIT" 6\)\)$`).MatchString(lines[0]) {
			t.Fatalf("%q", lines)
		}
	})
}

// TestEmptyCommandBADTranscript covers RFC 3501 §7.1.3, whose example is an
// empty command line: it gets an untagged BAD in every state, and the next
// command still runs. It is the upstream test of rest-mail/go-imap#176.
//
// checklist: IMAP-RESP-AUDIT-01
func TestEmptyCommandBADTranscript(t *testing.T) {
	for _, state := range []string{"not authenticated", "authenticated", "selected"} {
		t.Run(state, func(t *testing.T) {
			c := dial(t)
			c.line()
			if state != "not authenticated" {
				c.login()
			}
			if state == "selected" {
				if _, done := c.command("S1", "SELECT INBOX"); !strings.HasPrefix(done, "S1 OK ") {
					t.Fatal(done)
				}
			}
			// Pipeline a following command so a silently ignored blank cannot hang
			// this proof or be mistaken for an acceptable lack of response.
			if _, err := fmt.Fprint(c.conn, "\r\nN1 NOOP\r\n"); err != nil {
				t.Fatal(err)
			}
			if got := c.line(); !strings.HasPrefix(got, "* BAD ") {
				t.Fatalf("blank command: %q, want untagged BAD", got)
			}
			if got := c.line(); !strings.HasPrefix(got, "N1 OK ") {
				t.Fatalf("next command: %q", got)
			}
			if state == "selected" {
				if _, done := c.command("F1", "FETCH 1 (UID)"); !strings.HasPrefix(done, "F1 OK ") {
					t.Fatalf("selection lost: %q", done)
				}
			}
			if state == "authenticated" {
				if _, done := c.command("B1", `LIST "" *`); !strings.HasPrefix(done, "B1 OK ") {
					t.Fatalf("authentication lost: %q", done)
				}
			}
			if state == "not authenticated" && c.backend.calls != 0 {
				t.Fatal("blank command authenticated")
			}
		})
	}
}
