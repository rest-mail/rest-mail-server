package rfctest

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// rightsMailbox reports [imap.Rights] per folder: INBOX and Archive are the
// user's own, Shared may be read but not written, Dropbox written but not
// read, and Hidden neither. Projects/Plan is a folder below a Projects that is
// not one. A missing name may be created unless it starts with "Fixed". It is
// a UIDPLUS store with atomic MOVE, so every UID-disclosing reply is shown.
type rightsMailbox struct {
	*copyMailbox
	appended []string
	created  []string
}

var mailboxRights = map[string]imap.Rights{
	"INBOX":         {Read: true, Write: true},
	"Archive":       {Read: true, Write: true},
	"Shared":        {Read: true},
	"Dropbox":       {Write: true},
	"Hidden":        {},
	"Projects/Plan": {Read: true, Write: true},
}

func newRightsMailbox() *rightsMailbox { return &rightsMailbox{copyMailbox: newCopyMailbox()} }

func (*rightsMailbox) Folders() ([]imap.Folder, error) {
	var out []imap.Folder
	for _, name := range []string{"INBOX", "Archive", "Shared", "Dropbox", "Hidden", "Projects/Plan"} {
		out = append(out, imap.Folder{Name: name})
	}
	return out, nil
}

func (*rightsMailbox) MailboxRights(folder string) (imap.Rights, error) {
	if rights, ok := mailboxRights[folder]; ok {
		return rights, nil
	}
	return imap.Rights{Create: !strings.HasPrefix(folder, "Fixed")}, nil
}

// Append would create a missing destination implicitly, as some stores do:
// the engine must never let it.
func (m *rightsMailbox) Append(dest string, _ imap.FlagUpdate, _ []byte) error {
	if _, ok := mailboxRights[dest]; !ok {
		m.created = append(m.created, dest)
	}
	m.appended = append(m.appended, dest)
	return nil
}
func (*rightsMailbox) UIDValidity(string) (uint32, error) { return 38505, nil }
func (m *rightsMailbox) AppendUID(dest string, f imap.FlagUpdate, raw []byte) (uint32, error) {
	return 3955, m.Append(dest, f, raw)
}
func (m *rightsMailbox) CopyUID(uid uint32, dest string) (uint32, error) {
	return 4000 + uid, m.Copy(uid, dest)
}
func (m *rightsMailbox) MoveUID(uid uint32, dest string) (uint32, error) {
	return m.CopyUID(uid, dest)
}

// TestRightsSelectReadOnly proves that SELECT of a mailbox the user may read
// but not write opens it read-only with READ-ONLY and refuses changes, while
// SELECT of the user's own mailbox stays read-write and a mailbox the user may
// not read is not selected at all, nor its STATUS given.
// checklist: IMAP2-CMD-76 IMAP-SELECT-10
func TestRightsSelectReadOnly(t *testing.T) {
	c := selectionClient(t, newRightsMailbox())
	lines, tagged := c.command("S1", "SELECT Shared")
	if tagged != "S1 OK [READ-ONLY] SELECT completed" {
		t.Fatalf("SELECT Shared: %q", tagged)
	}
	requireLine(t, lines, "* OK [PERMANENTFLAGS ()] No permanent flags permitted")
	if _, tagged = c.command("S2", `STORE 1 +FLAGS (\Seen)`); !strings.HasPrefix(tagged, "S2 NO") {
		t.Fatalf("STORE on a read-only selection: %q", tagged)
	}
	if _, tagged = c.command("S3", "SELECT Archive"); tagged != "S3 OK [READ-WRITE] SELECT completed" {
		t.Fatalf("SELECT Archive: %q", tagged)
	}
	for _, cmd := range []string{"SELECT Dropbox", "EXAMINE Hidden", "STATUS Hidden (MESSAGES)"} {
		lines, tagged = c.command("S4", "%s", cmd)
		if !strings.HasPrefix(tagged, "S4 NO [NOPERM] ") {
			t.Fatalf("%s: %q", cmd, tagged)
		}
		for _, line := range lines {
			if strings.HasPrefix(line, "* STATUS") || strings.HasSuffix(line, "EXISTS") {
				t.Fatalf("%s disclosed %q", cmd, line)
			}
		}
	}
}

// TestRightsUIDDisclosure proves APPENDUID and COPYUID are sent for a
// destination the user may read and withheld, the command still succeeding,
// for one the user may write but not read.
// checklist: IMAP2-CMD-130 IMAP2-CMD-159 IMAP2-SEC-241 IMAP-APPENDUID-05
func TestRightsUIDDisclosure(t *testing.T) {
	m := newRightsMailbox()
	c := expungeClient(t, m)
	if _, st := appendCommand(t, c, "Archive", "message"); st != "A1 OK [APPENDUID 38505 3955] APPEND completed" {
		t.Fatalf("APPEND to a readable destination: %q", st)
	}
	if _, st := appendCommand(t, c, "Dropbox", "message"); st != "A1 OK APPEND completed" {
		t.Fatalf("APPEND to an unreadable destination: %q", st)
	}
	if _, tagged := c.command("C1", "COPY 1 Archive"); tagged != "C1 OK [COPYUID 38505 300 4300] COPY completed" {
		t.Fatalf("COPY to a readable destination: %q", tagged)
	}
	if _, tagged := c.command("C2", "UID COPY 300 Dropbox"); tagged != "C2 OK UID COPY completed" {
		t.Fatalf("COPY to an unreadable destination: %q", tagged)
	}
	lines, tagged := c.command("C3", "MOVE 1 Dropbox")
	requireExactly(t, lines, "* 1 EXPUNGE")
	if tagged != "C3 OK MOVE completed" {
		t.Fatalf("MOVE to an unreadable destination: %q", tagged)
	}
	lines, _ = c.command("C4", "MOVE 1 Archive")
	requireExactly(t, lines, "* OK [COPYUID 38505 304 4304] Moved", "* 1 EXPUNGE")
	if len(m.created) != 0 {
		t.Fatalf("created %q", m.created)
	}
}

// TestRightsTryCreate proves APPEND, COPY and MOVE to a mailbox that does not
// exist are refused before the store, which would create it, sees them: with
// TRYCREATE when the user could create it, plainly (NONEXISTENT) when creating
// it is known impossible. A destination the user may not write is NOPERM.
// checklist: IMAP2-CMD-129
func TestRightsTryCreate(t *testing.T) {
	m := newRightsMailbox()
	c := expungeClient(t, m)
	for dest, want := range map[string]string{
		"Missing":      "A1 NO [TRYCREATE] APPEND failed",
		"FixedMissing": "A1 NO [NONEXISTENT] APPEND failed",
		"Shared":       "A1 NO [NOPERM] APPEND failed",
	} {
		if lines, st := appendCommand(t, c, dest, "message"); st != want || len(lines) != 0 {
			t.Fatalf("APPEND %s: %q %q, want %q", dest, lines, st, want)
		}
	}
	for _, cmd := range []string{"COPY", "UID COPY", "MOVE", "UID MOVE"} {
		set := "1"
		if strings.HasPrefix(cmd, "UID") {
			set = "300"
		}
		if _, tagged := c.command("C1", "%s %s Missing", cmd, set); tagged != "C1 NO [TRYCREATE] "+cmd+" failed" {
			t.Fatalf("%s to a missing mailbox: %q", cmd, tagged)
		}
	}
	if len(m.appended) != 0 || len(m.created) != 0 || len(m.calls) != 0 {
		t.Fatalf("store saw appended=%q created=%q copies=%v", m.appended, m.created, m.calls)
	}
}

// TestRightsListStatusNoselect proves LIST-STATUS sends no STATUS for a
// mailbox the user may not read and marks it \Noselect, as plain LIST does,
// while a readable mailbox, even one that cannot be written, gets its STATUS.
// checklist: IMAP2-CMD-109
func TestRightsListStatusNoselect(t *testing.T) {
	c := selectionClient(t, newRightsMailbox())
	lines, tagged := c.command("L1", `LIST "" * RETURN (STATUS (MESSAGES))`)
	requireOK(t, tagged, "L1")
	for _, want := range []string{
		`* LIST () "/" "Shared"`, `* STATUS "Shared" (MESSAGES 4)`,
		`* LIST (\Noselect) "/" "Dropbox"`, `* LIST (\Noselect) "/" "Hidden"`,
	} {
		requireLine(t, lines, want)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, `* STATUS "Dropbox"`) || strings.HasPrefix(line, `* STATUS "Hidden"`) {
			t.Fatalf("STATUS for an unreadable mailbox: %q", line)
		}
	}
	lines, _ = c.command("L2", `LIST "" Hidden`)
	requireExactly(t, lines, `* LIST (\Noselect) "/" "Hidden"`)
}

// TestDeleteHasChildren proves DELETE of a name that is not a mailbox but has
// inferior mailboxes is refused with HASCHILDREN, while a mailbox with no
// inferiors is deleted.
// checklist: IMAP2-CMD-85
func TestDeleteHasChildren(t *testing.T) {
	c := selectionClient(t, newRightsMailbox())
	if _, tagged := c.command("D1", "DELETE Projects"); tagged != "D1 NO [HASCHILDREN] Mailbox has inferior mailboxes" {
		t.Fatalf("DELETE Projects: %q", tagged)
	}
	if _, tagged := c.command("D2", "DELETE Projects/Plan"); tagged != "D2 OK DELETE completed" {
		t.Fatalf("DELETE Projects/Plan: %q", tagged)
	}
}
