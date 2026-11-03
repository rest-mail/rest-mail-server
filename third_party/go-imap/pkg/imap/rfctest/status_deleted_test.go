package rfctest

import (
	"strconv"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// deletedFolder is blurdybloop with five messages, two of them \Deleted, and
// an empty INBOX.
type deletedFolder struct{ mailbox }

func (deletedFolder) Folders() ([]imap.Folder, error) {
	return []imap.Folder{{Name: "INBOX"}, {Name: "blurdybloop"}}, nil
}

func (deletedFolder) Messages(folder string) ([]imap.Message, error) {
	if folder != "blurdybloop" {
		return nil, nil
	}
	var msgs []imap.Message
	for i, deleted := range []bool{false, true, false, true, false} {
		msgs = append(msgs, imap.Message{UID: 44287 + uint32(i), Size: 100, Seen: true, Deleted: deleted})
	}
	return msgs, nil
}

// TestStatusDeleted: STATUS DELETED is "the number of messages that have the
// \Deleted flag set" (RFC 9051 §6.3.11), in the order asked beside the other
// items; it follows the §6.3.11 A042 example's form. An empty mailbox is
// DELETED 0, and LIST-STATUS takes DELETED too.
//
// checklist: IMAP2-DELTA-03
func TestStatusDeleted(t *testing.T) {
	c := selectionClient(t, deletedFolder{})
	lines, tagged := c.command("A042", "STATUS blurdybloop (UIDNEXT MESSAGES DELETED)")
	requireOK(t, tagged, "A042")
	requireExactly(t, lines, `* STATUS "blurdybloop" (UIDNEXT 44292 MESSAGES 5 DELETED 2)`)

	lines, tagged = c.command("A043", "STATUS INBOX (deleted)")
	requireOK(t, tagged, "A043")
	requireExactly(t, lines, `* STATUS "INBOX" (DELETED 0)`)

	lines, tagged = c.command("A044", `LIST "" * RETURN (STATUS (DELETED))`)
	requireOK(t, tagged, "A044")
	requireExactly(t, lines, `* LIST () "/" "INBOX"`, `* STATUS "INBOX" (DELETED 0)`, `* LIST () "/" "blurdybloop"`, `* STATUS "blurdybloop" (DELETED 2)`)
}

// TestSelectNoClosedForRev1: an IMAP4rev1 session is not sent the CLOSED
// response code when SELECT or EXAMINE replaces its selection. RFC 3501 asks
// that response codes outside the protocol "SHOULD be prefixed with an "X"
// until they are added to a revision of this protocol" (§7.1), and for rev1
// CLOSED belongs to RFC 7162 §3.2.11, which a server advertising neither
// CONDSTORE nor QRESYNC does not implement.
//
// checklist: IMAP-CLOSED-01
func TestSelectNoClosedForRev1(t *testing.T) {
	c := selectionClient(t, deletedFolder{})
	for i, cmd := range []string{"SELECT INBOX", "SELECT blurdybloop", "EXAMINE INBOX", "SELECT nosuch"} {
		lines, _ := c.command("B"+strconv.Itoa(i), "%s", cmd)
		for _, l := range lines {
			if l == "* OK [CLOSED] Previous mailbox is now closed" {
				t.Fatalf("%s: rev1 session was sent %q", cmd, l)
			}
		}
	}
}
