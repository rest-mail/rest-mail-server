package rfctest

import (
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// sizedFolder is frop with RFC 8438's eight messages, UIDNEXT 242344, and an
// empty INBOX.
type sizedFolder struct{ mailbox }

func (sizedFolder) Folders() ([]imap.Folder, error) {
	return []imap.Folder{{Name: "INBOX"}, {Name: "frop"}}, nil
}

func (sizedFolder) Messages(folder string) ([]imap.Message, error) {
	if folder != "frop" {
		return nil, nil
	}
	var msgs []imap.Message
	for i, size := range []int{3224, 1222, 444, 4516, 544, 922, 31126, 2423} {
		msgs = append(msgs, imap.Message{UID: 242336 + uint32(i), Size: size})
	}
	return msgs, nil
}

// TestStatusSize: with STATUS=SIZE advertised, STATUS takes SIZE, the
// mailbox's total size in octets, at least the sum of its messages'
// RFC822.SIZE (RFC 8438 §3): the RFC's A01 example, whose eight messages sum
// to 44421. An empty mailbox is SIZE 0, and LIST-STATUS takes SIZE too.
//
// checklist: IMAP-STATUSSIZE-01
// checklist: IMAP2-CMD-124
func TestStatusSize(t *testing.T) {
	c := selectionClient(t, sizedFolder{})
	requireCaps(t, c.capabilities("C1"), []string{"STATUS=SIZE"}, nil)
	lines, tagged := c.command("A01", "STATUS frop (MESSAGES SIZE UIDNEXT)")
	requireOK(t, tagged, "A01")
	requireExactly(t, lines, `* STATUS "frop" (MESSAGES 8 SIZE 44421 UIDNEXT 242344)`)

	lines, tagged = c.command("A02", "STATUS INBOX (size)")
	requireOK(t, tagged, "A02")
	requireExactly(t, lines, `* STATUS "INBOX" (SIZE 0)`)

	lines, tagged = c.command("A03", `LIST "" * RETURN (STATUS (SIZE))`)
	requireOK(t, tagged, "A03")
	requireExactly(t, lines, `* LIST () "/" "INBOX"`, `* STATUS "INBOX" (SIZE 0)`, `* LIST () "/" "frop"`, `* STATUS "frop" (SIZE 44421)`)
}
