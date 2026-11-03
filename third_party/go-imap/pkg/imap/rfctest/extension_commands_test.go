package rfctest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

type enableMailbox struct {
	mailbox
	calls int
	err   error
}

func (m *enableMailbox) EnableUTF8() error { m.calls++; return m.err }

// checklist: IMAP-EXT-CAP-01
func TestExtensionCapabilities(t *testing.T) {
	c := dial(t)
	c.line()
	for _, selected := range []string{"", "LOGIN", "SELECT"} {
		if selected == "LOGIN" {
			c.login()
		}
		if selected == "SELECT" {
			_, st := c.command("s", "SELECT INBOX")
			requireOK(t, st, "s")
		}
		lines, st := c.command("c", "CAPABILITY")
		requireOK(t, st, "c")
		if len(lines) != 1 {
			t.Fatalf("CAPABILITY: %q", lines)
		}
		for _, cap := range []string{"IDLE", "ENABLE", "UNSELECT"} {
			if !slices.Contains(strings.Fields(lines[0]), cap) {
				t.Fatalf("%s: missing %s: %q", selected, cap, lines)
			}
		}
	}
}

// checklist: IMAP-ENABLE-01 IMAP-ENABLE-02 IMAP-ENABLE-03
// checklist: IMAP2-CMD-63 IMAP2-CMD-64 IMAP2-CMD-66 IMAP2-CMD-70
// checklist: IMAP2-CMD-168
func TestEnableUnknownAndCapabilityStability(t *testing.T) {
	for _, box := range []imap.Mailbox{mailbox{}, &enableMailbox{}} {
		c := selectionClient(t, box)
		before, st := c.command("b", "CAPABILITY")
		requireOK(t, st, "b")
		for _, cmd := range []string{"ENABLE CONDSTORE X-GOOD-IDEA", "enable IDLE UNSELECT MOVE IMAP4rev1 AUTH=PLAIN"} {
			lines, st := c.command("e", "%s", cmd)
			requireOK(t, st, "e")
			requireExactly(t, lines, "* ENABLED")
		}
		after, st := c.command("a", "CAPABILITY")
		requireOK(t, st, "a")
		requireExactly(t, after, before...)
		if m, ok := box.(*enableMailbox); ok && m.calls != 0 {
			t.Fatalf("ignored caps enabled UTF8 %d times", m.calls)
		}
	}
}

// checklist: IMAP-ENABLE-02 IMAP-ENABLE-03 IMAP-ENABLE-04
// checklist: IMAP2-CMD-63 IMAP2-CMD-65 IMAP2-CMD-66 IMAP2-CMD-69 IMAP2-CMD-70
func TestEnableAdditiveAndPersistent(t *testing.T) {
	m := &enableMailbox{}
	c := selectionClient(t, m)
	before, st := c.command("b", "CAPABILITY")
	requireOK(t, st, "b")
	lines, st := c.command("a1", "EnAbLe X-UNKNOWN utf8=accept UTF8=ACCEPT")
	requireOK(t, st, "a1")
	requireExactly(t, lines, "* ENABLED UTF8=ACCEPT")
	for _, cmd := range []string{"ENABLE UTF8=ACCEPT", "ENABLE X-UNKNOWN", "SELECT INBOX", "UNSELECT", "ENABLE utf8=accept"} {
		lines, st := c.command("a2", "%s", cmd)
		requireOK(t, st, "a2")
		if strings.HasPrefix(cmd, "ENABLE") {
			requireExactly(t, lines, "* ENABLED")
		}
	}
	if m.calls != 1 {
		t.Fatalf("EnableUTF8 calls = %d, want 1 across selection and UNSELECT", m.calls)
	}
	after, st := c.command("a3", "CAPABILITY")
	requireOK(t, st, "a3")
	requireExactly(t, after, before...)
}

// checklist: IMAP-ENABLE-05
func TestEnableInvalidArguments(t *testing.T) {
	for _, args := range []string{"", " ", `"UTF8=ACCEPT"`, "(UTF8=ACCEPT)", "UTF8=ACCEPT X]BAD", "UTF8=ACCEPT X*BAD", "UTF8=ACCEPT X%BAD", "UTF8=ACCEPT X\\BAD", "UTF8=ACCEPT X\tBAD", "UTF8=ACCEPT X\x00BAD", "UTF8=ACCEPT Xé", "UTF8=ACCEPT  X-UNKNOWN", " UTF8=ACCEPT", "UTF8=ACCEPT "} {
		t.Run(fmt.Sprintf("%q", args), func(t *testing.T) {
			m := &enableMailbox{}
			c := selectionClient(t, m)
			cmd := "ENABLE"
			if args != "" {
				cmd += " " + args
			}
			lines, st := c.command("bad", "%s", cmd)
			if !strings.HasPrefix(st, "bad BAD ") {
				t.Fatalf("invalid ENABLE: %q / %q", lines, st)
			}
			requireExactly(t, lines)
			if m.calls != 0 {
				t.Fatalf("invalid list partially enabled UTF8: %d calls", m.calls)
			}
			lines, st = c.command("ok", "ENABLE UTF8=ACCEPT")
			requireOK(t, st, "ok")
			requireExactly(t, lines, "* ENABLED UTF8=ACCEPT")
			if m.calls != 1 {
				t.Fatalf("valid retry calls = %d", m.calls)
			}
		})
	}
}

// checklist: IMAP-ENABLE-06
func TestEnableStateAndFailure(t *testing.T) {
	m := &enableMailbox{err: errors.New("private backend detail")}
	c := dialMailbox(t, m)
	c.line()
	lines, st := c.command("pre", "ENABLE UTF8=ACCEPT")
	if !strings.HasPrefix(st, "pre NO ") {
		t.Fatalf("pre-auth ENABLE: %q", st)
	}
	requireExactly(t, lines)
	if m.calls != 0 {
		t.Fatal("pre-auth ENABLE reached backend")
	}
	c.login()
	lines, st = c.command("fail", "ENABLE UTF8=ACCEPT")
	if !strings.HasPrefix(st, "fail NO ") || strings.Contains(st, "private") {
		t.Fatalf("failed ENABLE: %q", st)
	}
	requireExactly(t, lines)
	m.err = nil
	lines, st = c.command("retry", "ENABLE UTF8=ACCEPT")
	requireOK(t, st, "retry")
	requireExactly(t, lines, "* ENABLED UTF8=ACCEPT")
	if m.calls != 2 {
		t.Fatalf("EnableUTF8 calls = %d, want failed attempt plus retry", m.calls)
	}
}

// checklist: IMAP-UNSELECT-01 IMAP-UNSELECT-02
func TestUnselectWithoutExpunge(t *testing.T) {
	for _, selectCmd := range []string{"SELECT", "EXAMINE"} {
		t.Run(selectCmd, func(t *testing.T) {
			m := newExpungeMailbox(2)
			c := selectionClient(t, m)
			_, st := c.command("s", "%s INBOX", selectCmd)
			requireOK(t, st, "s")
			if selectCmd == "SELECT" {
				markDeleted(t, c, "1:2")
			}
			lines, st := c.command("A341", "uNsElEcT")
			requireOK(t, st, "A341")
			requireExactly(t, lines)
			requireUIDs(t, m, 100, 110)
			if len(m.attempts) != 0 {
				t.Fatalf("UNSELECT deleted %v", m.attempts)
			}
			lines, st = c.command("f", "FETCH 1 FLAGS")
			if strings.HasPrefix(st, "f OK") || len(lines) != 0 {
				t.Fatalf("still selected: %q / %q", lines, st)
			}
			// Authentication survives; read-only access and old deletion marks do not.
			_, st = c.command("s2", "SELECT INBOX")
			requireOK(t, st, "s2")
			lines, st = c.command("e", "EXPUNGE")
			requireOK(t, st, "e")
			requireExactly(t, lines)
			markDeleted(t, c, "1")
			lines, st = c.command("e2", "EXPUNGE")
			requireOK(t, st, "e2")
			requireExactly(t, lines, "* 1 EXPUNGE")
			requireUIDs(t, m, 110)
		})
	}
}

// checklist: IMAP-UNSELECT-03
func TestUnselectRequiresSelection(t *testing.T) {
	c := dial(t)
	c.line()
	for _, login := range []bool{false, true} {
		if login {
			c.login()
		}
		lines, st := c.command("u", "UNSELECT")
		if !strings.HasPrefix(st, "u BAD ") {
			t.Fatalf("UNSELECT without selection: %q", st)
		}
		requireExactly(t, lines)
		_, st = c.command("n", "NOOP")
		requireOK(t, st, "n")
	}
}

// checklist: IMAP-UNSELECT-04
func TestUnselectInvalidArgumentsPreserveSelection(t *testing.T) {
	m := newExpungeMailbox(2)
	c := expungeClient(t, m)
	markDeleted(t, c, "1")
	for _, args := range []string{"INBOX", `""`, "(FLAGS)", " "} {
		lines, st := c.command("u", "UNSELECT %s", args)
		if !strings.HasPrefix(st, "u BAD ") {
			t.Fatalf("UNSELECT %q: %q", args, st)
		}
		requireExactly(t, lines)
		lines, st = c.command("f", "FETCH 1 (UID FLAGS)")
		requireOK(t, st, "f")
		requireExactly(t, lines, `* 1 FETCH (UID 100 FLAGS (\Deleted))`)
	}
	lines, st := c.command("e", "EXPUNGE")
	requireOK(t, st, "e")
	requireExactly(t, lines, "* 1 EXPUNGE")
	requireUIDs(t, m, 110)
}

// checklist: IMAP-IDLE-01
func TestIdleInvalidArguments(t *testing.T) {
	for _, args := range []string{"extra", `""`, " "} {
		c := expungeClient(t, newExpungeMailbox(1))
		if _, err := fmt.Fprintf(c.conn, "bad IDLE %s\r\n", args); err != nil {
			t.Fatal(err)
		}
		// Reading the first line also proves refusal precedes any continuation.
		if got := c.line(); !strings.HasPrefix(got, "bad BAD ") {
			t.Fatalf("IDLE %q first response: %q", args, got)
		}
		lines, st := c.command("f", "FETCH 1 (UID FLAGS)")
		requireOK(t, st, "f")
		requireExactly(t, lines, "* 1 FETCH (UID 100 FLAGS ())")
	}
}

// checklist: IMAP-IDLE-02 IMAP-RESP-17
// checklist: IMAP2-CMD-134
func TestIdleDoneAndNextCommand(t *testing.T) {
	c := expungeClient(t, newExpungeMailbox(1))
	for _, done := range []string{"DONE", "done"} {
		if _, err := fmt.Fprint(c.conn, "A002 idle\r\n"); err != nil {
			t.Fatal(err)
		}
		if got := c.line(); !strings.HasPrefix(got, "+ ") {
			t.Fatalf("IDLE continuation: %q", got)
		}
		wrote := make(chan error, 1)
		go func() { _, err := fmt.Fprintf(c.conn, "%s\r\nA003 NOOP\r\n", done); wrote <- err }()
		if got := c.line(); got != "A002 OK IDLE terminated" {
			t.Fatalf("IDLE completion: %q", got)
		}
		if got := c.line(); got != "A003 OK NOOP completed" {
			t.Fatalf("next command: %q", got)
		}
		if err := <-wrote; err != nil {
			t.Fatal(err)
		}
	}
}
