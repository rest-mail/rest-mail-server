package rfctest

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

type specialUseMailbox struct {
	mailbox
	folders []imap.Folder
	err     error
}

func (m *specialUseMailbox) Folders() ([]imap.Folder, error) {
	return slices.Clone(m.folders), m.err
}

// RFC 6154 permits special-use attributes on ordinary LIST. Each role is the
// store's: the backend names it on the folder, whatever the folder is called.
//
// checklist: IMAP-SPECIALUSE-01 IMAP-SPECIALUSE-02 IMAP-SPECIALUSE-03 IMAP-SPECIALUSE-04
func TestSpecialUseOrdinaryList(t *testing.T) {
	for _, selected := range []bool{false, true} {
		m := &specialUseMailbox{folders: []imap.Folder{
			{Name: "INBOX"},
			{Name: "Sent", SpecialUse: imap.SpecialUseSent},
			{Name: "Drafts", SpecialUse: imap.SpecialUseDrafts},
			{Name: "Trash", SpecialUse: imap.SpecialUseTrash},
			{Name: "Spam", SpecialUse: imap.SpecialUseJunk},
			{Name: "Archive", SpecialUse: imap.SpecialUseArchive},
			{Name: "Projects"},
		}}
		c := selectionClient(t, m)
		if selected {
			_, st := c.command("s", "SELECT INBOX")
			requireOK(t, st, "s")
		}
		lines, st := c.command("l", `LIST "" "*"`)
		requireOK(t, st, "l")
		requireExactly(t, lines,
			`* LIST () "/" "INBOX"`,
			`* LIST (\Sent) "/" "Sent"`,
			`* LIST (\Drafts) "/" "Drafts"`,
			`* LIST (\Trash) "/" "Trash"`,
			`* LIST (\Junk) "/" "Spam"`,
			`* LIST (\Archive) "/" "Archive"`,
			`* LIST () "/" "Projects"`)
		// An exact-name pattern retains the attribute; no other role leaks in.
		for name, role := range map[string]string{"Sent": `\Sent`, "Drafts": `\Drafts`, "Trash": `\Trash`, "Spam": `\Junk`, "Archive": `\Archive`} {
			lines, st = c.command("p", `LIST "" "%s"`, name)
			requireOK(t, st, "p")
			requireExactly(t, lines, `* LIST (`+role+`) "/" "`+name+`"`)
		}
	}
}

// The SPECIAL-USE selection option lists only the folders with a role, and the
// return option reports roles (RFC 6154 §2, with RFC 5258's extended LIST);
// an option this server does not know is refused rather than ignored.
//
// checklist: IMAP-SPECIALUSE-07
func TestSpecialUseExtendedList(t *testing.T) {
	m := &specialUseMailbox{folders: []imap.Folder{
		{Name: "INBOX"},
		{Name: "Sent", SpecialUse: imap.SpecialUseSent},
		{Name: "Spam", SpecialUse: imap.SpecialUseJunk},
		{Name: "Projects"},
	}}
	c := selectionClient(t, m)
	for _, cmd := range []string{`LIST (SPECIAL-USE) "" "*"`, `LIST (special-use) "" "*" RETURN (SPECIAL-USE)`} {
		lines, st := c.command("e", "%s", cmd)
		requireOK(t, st, "e")
		requireExactly(t, lines, `* LIST (\Sent) "/" "Sent"`, `* LIST (\Junk) "/" "Spam"`)
	}
	lines, st := c.command("r", `LIST "" "*" RETURN (SPECIAL-USE)`)
	requireOK(t, st, "r")
	requireExactly(t, lines,
		`* LIST () "/" "INBOX"`, `* LIST (\Sent) "/" "Sent"`,
		`* LIST (\Junk) "/" "Spam"`, `* LIST () "/" "Projects"`)
	for _, bad := range []string{
		`LIST (VIRTUAL) "" "*"`,
		`LIST "" "*" RETURN (MYRIGHTS)`,
		`LIST "" "*" EXTRA`,
		`LIST (SPECIAL-USE "" "*"`,
	} {
		lines, st := c.command("b", "%s", bad)
		if !strings.HasPrefix(st, "b BAD ") {
			t.Errorf("%s: %q, want BAD", bad, st)
		}
		requireExactly(t, lines)
	}
}

// SPECIAL-USE is advertised in every state (RFC 6154 §2); CREATE-SPECIAL-USE
// is not, as CREATE cannot assign a role.
//
// checklist: IMAP-SPECIALUSE-05
func TestSpecialUseCapabilityBoundary(t *testing.T) {
	c := dial(t)
	c.line()
	for _, state := range []string{"unauthenticated", "authenticated", "selected"} {
		if state == "authenticated" {
			c.login()
		}
		if state == "selected" {
			_, st := c.command("s", "SELECT INBOX")
			requireOK(t, st, "s")
		}
		caps := c.capabilities("c")
		if !slices.Contains(caps, "SPECIAL-USE") {
			t.Fatalf("%s does not advertise SPECIAL-USE: %q", state, caps)
		}
		if slices.Contains(caps, "CREATE-SPECIAL-USE") {
			t.Fatalf("%s advertises unsupported CREATE-SPECIAL-USE: %q", state, caps)
		}
	}
}

// checklist: IMAP-SPECIALUSE-06
func TestSpecialUseDoesNotInventFolders(t *testing.T) {
	m := &specialUseMailbox{}
	c := selectionClient(t, m)
	for _, pattern := range []string{"*", "Sent", "Drafts", "Trash", "Junk"} {
		lines, st := c.command("l", `LIST "" "%s"`, pattern)
		requireOK(t, st, "l")
		requireExactly(t, lines)
	}
	// Names alone do not create virtual/role mailboxes or give a role, not even
	// an exact one; an attribute RFC 6154 does not define is not reported.
	m.folders = []imap.Folder{{Name: "Sent"}, {Name: "sent"}, {Name: "Work/Sent"}, {Name: "Archive"}, {Name: "All"}, {Name: "Flagged"}, {Name: `Sent "old"`}, {Name: "Odd", SpecialUse: `\Important`}}
	lines, st := c.command("l", `LIST "" "*"`)
	requireOK(t, st, "l")
	requireExactly(t, lines,
		`* LIST () "/" "Sent"`, `* LIST () "/" "sent"`, `* LIST () "/" "Work/Sent"`,
		`* LIST () "/" "Archive"`, `* LIST () "/" "All"`,
		`* LIST () "/" "Flagged"`, `* LIST () "/" "Sent \"old\""`, `* LIST () "/" "Odd"`)
	m.err = errors.New("private folder failure")
	lines, st = c.command("l", `LIST "" "*"`)
	if !strings.HasPrefix(st, "l NO ") {
		t.Fatalf("folder failure: %q", st)
	}
	requireExactly(t, lines)
	m.err = nil
	lines, st = c.command("l", `LIST "" "Sent"`)
	requireOK(t, st, "l")
	requireExactly(t, lines, `* LIST () "/" "Sent"`)
}
