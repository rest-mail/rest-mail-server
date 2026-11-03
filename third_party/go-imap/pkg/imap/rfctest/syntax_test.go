package rfctest

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// statusWithCode is a status response that carries a response code: what
// follows the code's "]" must be SP and text.
var statusWithCode = regexp.MustCompile(`^\S+ (OK|NO|BAD|PREAUTH|BYE) \[[^\]]*\](.*)$`)

// requireCodeText fails unless every status response with a response code
// follows it with SP and at least one character of text.
func requireCodeText(t *testing.T, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if m := statusWithCode.FindStringSubmatch(l); m != nil && (len(m[2]) < 2 || m[2][0] != ' ') {
			t.Errorf("%q: a response code must be followed by SP and text", l)
		}
	}
}

// TestResponseCodesCarryText: RFC 3501 §9 has resp-text = ["[" resp-text-code
// "]" SP] text, and text = 1*TEXT-CHAR, so a status response never ends at its
// response code. SELECT's UNSEEN, UIDVALIDITY, UIDNEXT and PERMANENTFLAGS
// lines, EXAMINE's, and the COPYUID line MOVE sends before its EXPUNGEs each
// carry text after the code, as do the tagged completions.
//
// checklist: IMAP-SYNTAX-01
func TestResponseCodesCarryText(t *testing.T) {
	box := &sharedMailbox{messages: []imap.Message{{UID: 1}, {UID: 2, Seen: true}}}
	c := selectionClient(t, box)
	for _, command := range []string{"SELECT INBOX", "EXAMINE INBOX"} {
		lines, tagged := c.command("S1", "%s", command)
		requireOK(t, tagged, "S1")
		if len(lines) < 6 {
			t.Fatalf("%s: %q", command, lines)
		}
		requireCodeText(t, append(lines, tagged)...)
	}

	m := &uidMoveMailbox{moveMailbox: newMoveMailbox(), validity: 432432}
	mc := expungeClient(t, m)
	lines, tagged := mc.command("M1", "UID MOVE 304:319 foo")
	requireOK(t, tagged, "M1")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "* OK [COPYUID ") {
		t.Fatalf("UID MOVE: %q", lines)
	}
	requireCodeText(t, append(lines, tagged)...)
}

// TestTagGrammar: a tag is 1*<any ASTRING-CHAR except "+"> (RFC 3501 §9), so
// SP, CTL, 8-bit octets, "(", ")", "{", "%", "*", DQUOTE, backslash and "+"
// cannot be in one; "]" and "." can. A line whose tag is not a tag gets an
// untagged BAD, since there is no tag to answer with, and its command does not
// run: "* NOOP" cannot pass for an untagged response, nor "+" for a
// continuation.
//
// checklist: IMAP-SYNTAX-02
// checklist: IMAP2-CORE-05
func TestTagGrammar(t *testing.T) {
	c := dial(t)
	c.line()
	for _, tag := range []string{"a]1", "x.7", "A-1_b", "~!#$&'"} {
		if _, tagged := c.command(tag, "NOOP"); tagged != tag+" OK NOOP completed" {
			t.Fatalf("tag %q: %q", tag, tagged)
		}
	}
	for _, tag := range []string{"*", "+", "a+1", "a*1", "a%1", `a"1`, `a\1`, "a(1", "a)1", "a{1", "a\x7f1", "a\xe91"} {
		if _, err := fmt.Fprintf(c.conn, "%s LOGIN %s %s\r\n", tag, testUser, testPass); err != nil {
			t.Fatal(err)
		}
		if got := c.line(); got != "* BAD Invalid tag" {
			t.Fatalf("tag %q: %q, want an untagged BAD", tag, got)
		}
	}
	if c.backend.calls != 0 {
		t.Fatalf("a command with an invalid tag ran: %d sign-ins", c.backend.calls)
	}
	if _, tagged := c.command("a2", "NOOP"); tagged != "a2 OK NOOP completed" {
		t.Fatalf("after the refusals: %q", tagged)
	}
}

// TestCommandsWithoutArguments: CAPABILITY, LOGOUT and NOOP (command-any),
// STARTTLS (command-nonauth), CHECK, CLOSE and EXPUNGE (command-select), IDLE
// (RFC 2177) and UNSELECT (RFC 3691) end at their name, and SP only separates
// (RFC 3501 §9: "It is NOT permitted to ... insert additional spaces"). So
// anything after the name, even one trailing space, gets BAD, and the command
// does not run: LOGOUT keeps the session, CLOSE and UNSELECT keep the
// selection, IDLE sends no continuation.
//
// checklist: IMAP-SYNTAX-03
// checklist: IMAP2-CORE-05
func TestCommandsWithoutArguments(t *testing.T) {
	box := &sharedMailbox{messages: []imap.Message{{UID: 1}}}
	c := sharedClient(t, box)
	for _, cmd := range []string{"CAPABILITY", "LOGOUT", "NOOP", "STARTTLS", "CHECK", "CLOSE", "EXPUNGE", "IDLE", "UNSELECT"} {
		for _, after := range []string{" ", " x", " (x)"} {
			lines, tagged := c.command("B1", "%s%s", strings.ToLower(cmd), after)
			if tagged != "B1 BAD "+cmd+" takes no arguments" || len(lines) != 0 {
				t.Fatalf("%q: %q %q", cmd+after, lines, tagged)
			}
		}
	}
	lines, tagged := c.command("F1", "FETCH 1 (UID)")
	requireOK(t, tagged, "F1")
	requireExactly(t, lines, "* 1 FETCH (UID 1 FLAGS ())")
}

// TestLoginArguments: LOGIN is "LOGIN" SP userid SP password, each an astring
// (RFC 3501 §9). An atom holds no atom-special and no 8-bit octet, and a
// quoted string escapes only DQUOTE and backslash. A missing, extra or doubled
// argument separator, or an argument that is not an astring, gets BAD without
// the credentials being checked; quoted and atom forms of the right ones sign
// in.
//
// checklist: IMAP-SYNTAX-04
// checklist: IMAP2-CORE-05
func TestLoginArguments(t *testing.T) {
	c := dial(t)
	c.line()
	for _, args := range []string{
		testUser,
		testUser + " ",
		testUser + "  " + testPass,
		" " + testUser + " " + testPass,
		testUser + " " + testPass + " extra",
		testUser + " " + testPass + " ",
		testUser + " {6a}",
		testUser + " s3cr(t",
		testUser + " s3cr%t",
		"alic\xe9 " + testPass,
		testUser + ` "s3\cret"`,
		testUser + ` "s3cret`,
		testUser + ` "s3cret"x`,
	} {
		if _, tagged := c.command("L0", "LOGIN %s", args); !strings.HasPrefix(tagged, "L0 BAD ") {
			t.Fatalf("LOGIN %q: %q, want BAD", args, tagged)
		}
	}
	if c.backend.calls != 0 {
		t.Fatalf("refused LOGINs checked credentials %d times", c.backend.calls)
	}
	if _, tagged := c.command("L1", `LOGIN "%s" "%s"`, testUser, `s3cret`); !strings.HasPrefix(tagged, "L1 OK ") {
		t.Fatalf("quoted LOGIN: %q", tagged)
	}
}

// TestCaseAndSpacing: command names and keywords are case-insensitive, SP is
// exactly one space, never a TAB or several, and NUL "MUST NOT be used at any
// time" (RFC 3501 §9, notes 1-3). Lower- and mixed-case commands run; a doubled
// space, a TAB or a NUL gets BAD.
//
// checklist: IMAP-SYNTAX-05
// checklist: IMAP2-CORE-05
// checklist: IMAP2-SEC-218 IMAP2-SEC-219
func TestCaseAndSpacing(t *testing.T) {
	box := &sharedMailbox{messages: []imap.Message{{UID: 1}}}
	c := dialMailbox(t, box)
	c.line()
	if _, tagged := c.command("a1", "login %s %s", testUser, testPass); !strings.HasPrefix(tagged, "a1 OK ") {
		t.Fatalf("login: %q", tagged)
	}
	for _, command := range []string{"Select inbox", "fetch 1 (flags)", "uid Fetch 1:* Flags", "search all", "Status inbox (messages)", "noop"} {
		if _, tagged := c.command("a2", "%s", command); !strings.HasPrefix(tagged, "a2 OK ") {
			t.Fatalf("%q: %q", command, tagged)
		}
	}
	for _, command := range []string{" NOOP", "FETCH  1 FLAGS", "FETCH 1  FLAGS", "FETCH\t1 FLAGS", "FETCH 1 (FLAGS  UID)", "NOOP\x00", "STATUS INBOX (MESSAGES  UIDNEXT)"} {
		if _, err := fmt.Fprintf(c.conn, "a3 %s\r\n", command); err != nil {
			t.Fatal(err)
		}
		if got := c.line(); !strings.HasPrefix(got, "a3 BAD ") {
			t.Fatalf("%q: %q, want BAD", command, got)
		}
	}
	if _, err := fmt.Fprint(c.conn, "a4\tNOOP\r\n"); err != nil {
		t.Fatal(err)
	}
	if got := c.line(); got != "* BAD Missing tag or command" {
		t.Fatalf("a TAB after the tag: %q", got)
	}
}
