package rfctest

import (
	"errors"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// uidNextMailbox is a store that remembers the UIDs it has assigned: next is
// one above the highest, and an expunge does not lower it (RFC 3501
// §2.3.1.1).
type uidNextMailbox struct {
	*expungeMailbox
	next uint32
	err  error
}

func (m *uidNextMailbox) UIDNext(string) (uint32, error) { return m.next, m.err }
func (m *uidNextMailbox) Append(string, imap.FlagUpdate, []byte) error {
	m.messages = append(m.messages, imap.Message{UID: m.next})
	m.next++
	return nil
}

func newUIDNextMailbox() *uidNextMailbox {
	// UIDs 100, 110 and 120 are present; 121 is the next the store assigns.
	return &uidNextMailbox{expungeMailbox: newExpungeMailbox(3), next: 121}
}

func selectUIDNext(t *testing.T, c *client, cmd string) string {
	t.Helper()
	lines, tagged := c.command("U", "%s INBOX", cmd)
	requireOK(t, tagged, "U")
	for _, line := range lines {
		if strings.HasPrefix(line, "* OK [UIDNEXT ") {
			return line
		}
	}
	t.Fatalf("%s sent no UIDNEXT: %q", cmd, lines)
	return ""
}

func statusUIDNext(t *testing.T, c *client) string {
	t.Helper()
	lines, tagged := c.command("T", "STATUS INBOX (UIDNEXT)")
	requireOK(t, tagged, "T")
	requireExactly(t, lines, lines[0])
	return lines[0]
}

// TestUIDNextSurvivesExpunge: expunging the newest message leaves UIDNEXT
// where it was in SELECT, EXAMINE and STATUS, and a message added afterwards
// moves it on, above the expunged UID (RFC 3501 §2.3.1.1, RFC 9051 §2.3.1.1).
//
// checklist: IMAP-SELECT-09 IMAP-STATUS-08 IMAP2-CORE-09
func TestUIDNextSurvivesExpunge(t *testing.T) {
	m := newUIDNextMailbox()
	c := expungeClient(t, m)
	if got := statusUIDNext(t, c); got != "* STATUS \"INBOX\" (UIDNEXT 121)" {
		t.Fatalf("before expunge: %q", got)
	}

	markDeleted(t, c, "3")
	_, tagged := c.command("E", "EXPUNGE")
	requireOK(t, tagged, "E")
	requireUIDs(t, m.expungeMailbox, 100, 110)

	for _, cmd := range []string{"SELECT", "EXAMINE"} {
		if got := selectUIDNext(t, c, cmd); got != "* OK [UIDNEXT 121] Predicted next UID" {
			t.Fatalf("%s after expunging UID 120: %q", cmd, got)
		}
	}
	if got := statusUIDNext(t, c); got != "* STATUS \"INBOX\" (UIDNEXT 121)" {
		t.Fatalf("STATUS after expunging UID 120: %q", got)
	}

	_, tagged = c.command("A", "APPEND INBOX {2+}\r\nhi")
	requireOK(t, tagged, "A")
	requireUIDs(t, m.expungeMailbox, 100, 110, 121)
	if got := statusUIDNext(t, c); got != "* STATUS \"INBOX\" (UIDNEXT 122)" {
		t.Fatalf("STATUS after APPEND: %q", got)
	}
	if got := selectUIDNext(t, c, "SELECT"); got != "* OK [UIDNEXT 122] Predicted next UID" {
		t.Fatalf("SELECT after APPEND: %q", got)
	}
}

// TestUIDNextFallsBackToHighestUID: a store that does not know (0), or that
// answers below a UID it holds, never makes UIDNEXT fall to or below a
// present UID; a Mailbox without UIDNextMailbox keeps highest UID plus one.
//
// checklist: IMAP-SELECT-09 IMAP-STATUS-08
func TestUIDNextFallsBackToHighestUID(t *testing.T) {
	for name, box := range map[string]imap.Mailbox{
		"without the interface": newExpungeMailbox(3),
		"store answers 0":       &uidNextMailbox{expungeMailbox: newExpungeMailbox(3)},
		"store answers low":     &uidNextMailbox{expungeMailbox: newExpungeMailbox(3), next: 105},
	} {
		t.Run(name, func(t *testing.T) {
			c := selectionClient(t, box)
			if got := selectUIDNext(t, c, "SELECT"); got != "* OK [UIDNEXT 121] Predicted next UID" {
				t.Fatalf("SELECT: %q", got)
			}
			if got := statusUIDNext(t, c); got != "* STATUS \"INBOX\" (UIDNEXT 121)" {
				t.Fatalf("STATUS: %q", got)
			}
		})
	}
}

// TestUIDNextStoreErrorRefuses: when the store cannot say what it has
// assigned, SELECT, EXAMINE and STATUS answer NO rather than report a UIDNEXT
// that may have gone down. STATUS without UIDNEXT still answers.
//
// checklist: IMAP-SELECT-09 IMAP-STATUS-08
func TestUIDNextStoreErrorRefuses(t *testing.T) {
	m := newUIDNextMailbox()
	m.err = errors.New("database unavailable")
	c := selectionClient(t, m)
	for _, cmd := range []string{"SELECT INBOX", "EXAMINE INBOX", "STATUS INBOX (UIDNEXT)"} {
		lines, tagged := c.command("N", "%s", cmd)
		if !strings.HasPrefix(tagged, "N NO") {
			t.Fatalf("%s: %q %q, want NO", cmd, lines, tagged)
		}
	}
	// A STATUS that does not ask for UIDNEXT does not need the store's answer.
	lines, tagged := c.command("M", "STATUS INBOX (MESSAGES)")
	requireOK(t, tagged, "M")
	requireExactly(t, lines, "* STATUS \"INBOX\" (MESSAGES 3)")
}
