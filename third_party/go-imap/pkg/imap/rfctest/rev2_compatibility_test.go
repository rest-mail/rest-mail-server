package rfctest

import (
	"reflect"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

type shiftCaseMailbox struct {
	*selectionMailbox
	opened []string
}

func (*shiftCaseMailbox) Folders() ([]imap.Folder, error) {
	return []imap.Folder{{Name: "é"}, {Name: "ʉ"}}, nil
}

func (m *shiftCaseMailbox) Messages(folder string) ([]imap.Message, error) {
	m.opened = append(m.opened, folder)
	return m.selectionMailbox.Messages(folder)
}

// The ASCII case of modified-base64 payload is significant: AOk encodes U+00E9
// while Aok encodes U+0289. The two names must reach different backend folders
// and be written back with their distinct shift sequences intact.
// checklist: IMAP2-COMPAT-253
func TestModifiedUTF7ShiftCaseIsSignificant(t *testing.T) {
	box := &shiftCaseMailbox{selectionMailbox: &selectionMailbox{folders: map[string][]imap.Message{
		"é": {{UID: 1}}, "ʉ": {{UID: 7}},
	}}}
	c := selectionClient(t, box)
	for _, tc := range []struct{ name, uidnext string }{{"&AOk-", "2"}, {"&Aok-", "8"}} {
		lines, completion := c.command("S1", "SELECT %s", tc.name)
		requireOK(t, completion, "S1")
		requireLine(t, lines, "* OK [UIDNEXT "+tc.uidnext+"] Predicted next UID")
	}
	if !reflect.DeepEqual(box.opened, []string{"é", "ʉ"}) {
		t.Fatalf("backend folder identities changed: %q", box.opened)
	}
	lines, completion := c.command("L1", `LIST "" "*"`)
	requireOK(t, completion, "L1")
	requireExactly(t, lines, `* LIST () "/" "&AOk-"`, `* LIST () "/" "&Aok-"`)
}
