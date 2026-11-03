package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestMailboxNamedNIL: "NIL is never used for any data item which takes the
// form of an atom. For example, a mailbox name of "NIL" is a mailbox named NIL
// as opposed to a non-existent mailbox name" (RFC 3501 §4.5). A folder called
// NIL is listed and answered as a quoted name, which no client can read as
// NIL, and SELECT, EXAMINE and STATUS find it by that name, bare or quoted, in
// any case only as written.
//
// checklist: IMAP-NIL-01
// checklist: IMAP2-RESP-189
func TestMailboxNamedNIL(t *testing.T) {
	box := newNamesMailbox("NIL")
	c := selectionClient(t, box)

	lines, tagged := c.command("L1", `LIST "" "*"`)
	requireOK(t, tagged, "L1")
	requireLine(t, lines, `* LIST () "/" "NIL"`)

	for i, cmd := range []string{"SELECT NIL", `EXAMINE "NIL"`} {
		tag := fmt.Sprintf("S%d", i+1)
		_, tagged := c.command(tag, "%s", cmd)
		requireOK(t, tagged, tag)
	}
	lines, tagged = c.command("S3", "STATUS NIL (MESSAGES)")
	requireOK(t, tagged, "S3")
	requireExactly(t, lines, `* STATUS "NIL" (MESSAGES 1)`)
	if _, tagged = c.command("S4", "SELECT nil"); !strings.HasPrefix(tagged, "S4 NO ") {
		t.Fatalf("SELECT nil: %q, want NO: only INBOX is case-insensitive", tagged)
	}
	if want := []string{"NIL", "NIL", "NIL", "nil"}; !slices.Equal(box.reads, want) {
		t.Fatalf("backend reads %q, want %q", box.reads, want)
	}
}
