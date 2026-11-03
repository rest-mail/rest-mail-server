package rfctest

import (
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// listFolders is RFC 5258 §5's hierarchy, with Music/Rock under a Music that
// is not a mailbox, as example 11 has, Sent with its role, and Archive/2026
// with the archive role under an Archive that is not a mailbox.
type listFolders struct{ mailbox }

func (listFolders) Folders() ([]imap.Folder, error) {
	return []imap.Folder{
		{Name: "INBOX"}, {Name: "Fruit"}, {Name: "Fruit/Apple"}, {Name: "Fruit/Banana"},
		{Name: "Tofu"}, {Name: "Vegetable"}, {Name: "Vegetable/Broccoli"},
		{Name: "Music/Rock"}, {Name: "Sent", SpecialUse: imap.SpecialUseSent},
		{Name: "Archive/2026", SpecialUse: imap.SpecialUseArchive},
	}, nil
}

// TestListHierarchyLevels: "If the "%" wildcard is the last character of a
// mailbox name argument, matching levels of hierarchy are also returned. If
// these levels of hierarchy are not also selectable mailboxes, they are
// returned with the \Noselect mailbox name attribute" (RFC 3501 §6.3.8). With
// "*" every name below is listed itself, so the level is not.
//
// checklist: IMAP-LIST-06
// checklist: IMAP2-RESP-187
func TestListHierarchyLevels(t *testing.T) {
	c := selectionClient(t, listFolders{})
	runExactly(t, c, `LIST "" "%"`,
		`* LIST () "/" "INBOX"`, `* LIST () "/" "Fruit"`, `* LIST () "/" "Tofu"`, `* LIST () "/" "Vegetable"`,
		`* LIST (\Noselect) "/" "Music"`, `* LIST (\Sent) "/" "Sent"`, `* LIST (\Noselect) "/" "Archive"`)
	runExactly(t, c, `LIST "" "Mus*"`, `* LIST () "/" "Music/Rock"`)
	runExactly(t, c, `LIST "" "Music/%"`, `* LIST () "/" "Music/Rock"`)
}

// TestListExtendedChildren: the CHILDREN return option (RFC 5258 §4) gives
// every reply \HasChildren or \HasNoChildren, never both (example 3); a level
// that is not a mailbox but has one below it that is not matched is
// \NonExistent \HasChildren (example 11), and gets no STATUS (RFC 5819 §2).
// REMOTE asks for remote mailboxes too, and this server has none (example 4).
//
// checklist: IMAP-LISTEXT-01 IMAP-LISTEXT-02
// checklist: IMAP2-CMD-100 IMAP2-CMD-104 IMAP2-CMD-108 IMAP2-CMD-111 IMAP2-CMD-113 IMAP2-CMD-114
// checklist: IMAP2-RESP-187
func TestListExtendedChildren(t *testing.T) {
	c := selectionClient(t, listFolders{})
	requireCaps(t, c.capabilities("C1"), []string{"LIST-EXTENDED"}, nil)
	want := []string{
		`* LIST (\HasNoChildren) "/" "INBOX"`, `* LIST (\HasChildren) "/" "Fruit"`, `* LIST (\HasNoChildren) "/" "Tofu"`,
		`* LIST (\HasChildren) "/" "Vegetable"`, `* LIST (\NonExistent \HasChildren) "/" "Music"`,
		`* LIST (\Sent \HasNoChildren) "/" "Sent"`, `* LIST (\NonExistent \HasChildren) "/" "Archive"`,
	}
	runExactly(t, c, `LIST () "" "%" RETURN (CHILDREN)`, want...)
	runExactly(t, c, `list (remote) "" "%" return (children)`, want...)
	runExactly(t, c, `LIST "" "%" RETURN (STATUS (MESSAGES))`,
		`* LIST () "/" "INBOX"`, `* STATUS "INBOX" (MESSAGES 1)`, `* LIST () "/" "Fruit"`, `* STATUS "Fruit" (MESSAGES 0)`,
		`* LIST () "/" "Tofu"`, `* STATUS "Tofu" (MESSAGES 0)`, `* LIST () "/" "Vegetable"`, `* STATUS "Vegetable" (MESSAGES 0)`,
		`* LIST (\NonExistent \HasChildren) "/" "Music"`, `* LIST (\Sent) "/" "Sent"`, `* STATUS "Sent" (MESSAGES 0)`,
		`* LIST (\NonExistent \HasChildren) "/" "Archive"`)
}

// TestListExtendedSubscribed: every folder this server has is subscribed, as
// LSUB says; the SUBSCRIBED selection option lists the subscribed names with
// \Subscribed (example 2), the SUBSCRIBED return option marks them on any
// LIST (example 6), and attributes stay accurate and additive (§§3.1-3.3).
//
// checklist: IMAP-LISTEXT-03
func TestListExtendedSubscribed(t *testing.T) {
	c := selectionClient(t, listFolders{})
	all := []string{
		`* LIST (\Subscribed) "/" "INBOX"`, `* LIST (\Subscribed) "/" "Fruit"`, `* LIST (\Subscribed) "/" "Fruit/Apple"`,
		`* LIST (\Subscribed) "/" "Fruit/Banana"`, `* LIST (\Subscribed) "/" "Tofu"`, `* LIST (\Subscribed) "/" "Vegetable"`,
		`* LIST (\Subscribed) "/" "Vegetable/Broccoli"`, `* LIST (\Subscribed) "/" "Music/Rock"`,
		`* LIST (\Sent \Subscribed) "/" "Sent"`, `* LIST (\Archive \Subscribed) "/" "Archive/2026"`,
	}
	runExactly(t, c, `LIST (SUBSCRIBED) "" "*"`, all...)
	runExactly(t, c, `LIST "" "*" RETURN (SUBSCRIBED)`, all...)
	runExactly(t, c, `LIST (SUBSCRIBED) "" "Fruit/%" RETURN (CHILDREN)`,
		`* LIST (\Subscribed \HasNoChildren) "/" "Fruit/Apple"`, `* LIST (\Subscribed \HasNoChildren) "/" "Fruit/Banana"`)
}

// TestListExtendedRecursiveMatch: RECURSIVEMATCH also returns a matched name
// whose descendant meets the selection without being matched, with CHILDINFO
// naming the criteria (RFC 5258 §§3.1, 3.5; example 8): Archive, not a mailbox,
// for its \Archive child; Fruit, which itself meets SUBSCRIBED, for its
// children. A name with no such descendant, and one that does not match, is
// not returned.
//
// checklist: IMAP-LISTEXT-02 IMAP-LISTEXT-04
// checklist: IMAP2-CMD-100 IMAP2-CMD-113 IMAP2-CMD-115
func TestListExtendedRecursiveMatch(t *testing.T) {
	c := selectionClient(t, listFolders{})
	runExactly(t, c, `LIST (SPECIAL-USE RECURSIVEMATCH) "" "%"`,
		`* LIST (\Sent) "/" "Sent"`, `* LIST (\NonExistent) "/" "Archive" ("CHILDINFO" ("SPECIAL-USE"))`)
	runExactly(t, c, `LIST (SUBSCRIBED RECURSIVEMATCH) "" "%"`,
		`* LIST (\Subscribed) "/" "INBOX"`, `* LIST (\Subscribed) "/" "Fruit" ("CHILDINFO" ("SUBSCRIBED"))`,
		`* LIST (\Subscribed) "/" "Tofu"`, `* LIST (\Subscribed) "/" "Vegetable" ("CHILDINFO" ("SUBSCRIBED"))`,
		`* LIST (\NonExistent) "/" "Music" ("CHILDINFO" ("SUBSCRIBED"))`, `* LIST (\Sent \Subscribed) "/" "Sent"`,
		`* LIST (\NonExistent) "/" "Archive" ("CHILDINFO" ("SUBSCRIBED"))`)
	// Without RECURSIVEMATCH the level above Archive/2026 still says there is
	// one below, as example 11 shows.
	runExactly(t, c, `LIST (SPECIAL-USE) "" "%"`, `* LIST (\Sent) "/" "Sent"`, `* LIST (\NonExistent \HasChildren) "/" "Archive"`)
}

// TestListMultiplePatterns: several patterns in parentheses, a name matching
// any of them answered once (RFC 5258 §3, example 7); an empty pattern in an
// extended LIST is ignored, never the delimiter request (§3).
//
// checklist: IMAP-LISTEXT-05
// checklist: IMAP2-CMD-95 IMAP2-CMD-100
func TestListMultiplePatterns(t *testing.T) {
	c := selectionClient(t, listFolders{})
	runExactly(t, c, `LIST "" ("INBOX" "Fruit/%" "Fruit/Apple")`,
		`* LIST () "/" "INBOX"`, `* LIST () "/" "Fruit/Apple"`, `* LIST () "/" "Fruit/Banana"`)
	runExactly(t, c, `LIST "" ("" "Tofu")`, `* LIST () "/" "Tofu"`)
	runExactly(t, c, `LIST () "" ""`)
	runExactly(t, c, `LIST "" ""`, `* LIST (\Noselect) "/" ""`)
}

// TestListExtendedRefusals: "A server MUST respond to options it does not
// recognize with a BAD response", and RECURSIVEMATCH alone, or with only
// REMOTE, is BAD (RFC 5258 §§3, 3.1); so is an unterminated pattern list.
//
// checklist: IMAP-LISTEXT-06
// checklist: IMAP2-CMD-105
func TestListExtendedRefusals(t *testing.T) {
	c := selectionClient(t, listFolders{})
	for _, command := range []string{
		`LIST (VIRTUAL) "" "*"`, `LIST "" "*" RETURN (MYRIGHTS)`, `LIST (RECURSIVEMATCH) "" "%"`,
		`LIST (REMOTE RECURSIVEMATCH) "" "%"`, `LIST "" ("INBOX" "Tofu"`,
	} {
		if lines, tagged := c.command("E1", "%s", command); !strings.HasPrefix(tagged, "E1 BAD") || len(lines) != 0 {
			t.Fatalf("%s: %q %q, want BAD", command, lines, tagged)
		}
	}
}
