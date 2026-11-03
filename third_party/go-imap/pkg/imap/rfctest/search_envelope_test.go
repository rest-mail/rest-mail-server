package rfctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// TestSearchFromMatchesTheEnvelopeMailbox: FROM matches "the envelope
// structure's FROM field" (RFC 3501 §6.4.4), which carries the sender's display
// name as well as the address. A name matches as an address does, an encoded
// word in it is decoded first, and a string may span the two ("Name <address>").
// The search reads only the message's metadata.
//
// checklist: IMAP-SEARCH-26
func TestSearchFromMatchesTheEnvelopeMailbox(t *testing.T) {
	box := newFetchMailbox()
	box.messages = []imap.Message{
		{UID: 10, From: imap.Address{Name: "Anne Smith", Email: "anne@example.com"}},
		{UID: 40, From: imap.Address{Email: "smith@example.org"}},
		{UID: 90, From: imap.Address{Name: "=?UTF-8?Q?J=C3=B6rg_Brown?=", Email: "jb@example.net"}},
		{UID: 150, From: imap.Address{Name: "Bob", Email: "bob@example.com"}},
	}
	c := fetchClient(t, box, "SELECT")
	for _, tc := range []struct{ query, seqs, uids string }{
		{`FROM "Smith"`, "1 2", "10 40"},
		{`FROM "anne smith"`, "1", "10"},
		{`FROM "Smith <anne@"`, "1", "10"},
		{`FROM "brown"`, "3", "90"},
		{`FROM "=?UTF-8?"`, "", ""},
		{`FROM "bob@example.com"`, "4", "150"},
		{`NOT FROM "Smith"`, "3 4", "90 150"},
		{`OR FROM "Bob" FROM "Brown"`, "3 4", "90 150"},
		{`FROM "<"`, "1 3 4", "10 90 150"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			requireSearch(t, c, "SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.seqs))
			requireSearch(t, c, "UID SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.uids))
		})
	}
	if len(box.fetches) != 0 || len(box.updates) != 0 {
		t.Fatalf("FROM accessed content or changed flags: fetches=%v updates=%v", box.fetches, box.updates)
	}
}

// TestSearchNewAndOld: NEW "is functionally equivalent to (RECENT UNSEEN)" and
// OLD to "NOT RECENT (as opposed to NOT NEW)" (RFC 3501 §6.4.4). Both are search
// keys without an operand, case-insensitive, and combine with other keys; each
// answers exactly what its equivalent answers, so they follow RECENT. This
// engine's RECENT is the unseen set (IMAP-SEARCH-12), which makes NEW the unseen
// messages and OLD the seen ones.
//
// checklist: IMAP-SEARCH-27
func TestSearchNewAndOld(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/empty=%t", selection, empty), func(t *testing.T) {
				box := newFetchMailbox()
				box.messages = []imap.Message{
					{UID: 10, Seen: true},
					{UID: 40},
					{UID: 90, Seen: true, Flagged: true},
					{UID: 150, Flagged: true},
				}
				if empty {
					box.messages = nil
				}
				c := fetchClient(t, box, selection)
				for _, tc := range []struct{ query, equivalent, seqs, uids string }{
					{"NEW", "(RECENT UNSEEN)", "2 4", "40 150"},
					{"nEw", "(RECENT UNSEEN)", "2 4", "40 150"},
					{"OLD", "NOT RECENT", "1 3", "10 90"},
					{"oLd", "NOT RECENT", "1 3", "10 90"},
					{"NEW FLAGGED", "(RECENT UNSEEN) FLAGGED", "4", "150"},
					{"OLD FLAGGED", "NOT RECENT FLAGGED", "3", "90"},
					{"NEW 2:3", "(RECENT UNSEEN) 2:3", "2", "40"},
					{"OR NEW OLD", "OR (RECENT UNSEEN) NOT RECENT", "1 2 3 4", "10 40 90 150"},
					{"NOT NEW", "NOT (RECENT UNSEEN)", "1 3", "10 90"},
					{"NOT OLD", "RECENT", "2 4", "40 150"},
					{"(OLD) UID 1:90", "(NOT RECENT) UID 1:90", "1 3", "10 90"},
					{"NEW OLD", "(RECENT UNSEEN) NOT RECENT", "", ""},
				} {
					t.Run(tc.query, func(t *testing.T) {
						seqs, uids := tc.seqs, tc.uids
						if empty {
							seqs, uids = "", ""
						}
						for _, query := range []string{tc.query, tc.equivalent} {
							requireSearch(t, c, "SEARCH "+query, strings.TrimSpace("* SEARCH "+seqs))
							requireSearch(t, c, "UID SEARCH "+query, strings.TrimSpace("* SEARCH "+uids))
						}
					})
				}
				if len(box.fetches) != 0 || len(box.updates) != 0 {
					t.Fatalf("NEW/OLD accessed content or changed flags: fetches=%v updates=%v", box.fetches, box.updates)
				}
			})
		}
	}
}

// TestSearchDeletedUnflaggedAndTo: DELETED and UNDELETED follow the \Deleted
// flag a STORE sets and clears in the session, UNFLAGGED is the complement of
// FLAGGED, and TO matches a case-insensitive substring of the envelope's TO
// field, with encoded words decoded first (RFC 3501 §6.4.4). None reads message
// content.
//
// checklist: IMAP-SEARCH-28
func TestSearchDeletedUnflaggedAndTo(t *testing.T) {
	box := newFetchMailbox()
	box.messages = []imap.Message{
		{UID: 10, Flagged: true, To: "Anne <anne@example.com>"},
		{UID: 40, To: "bob@example.org, carol@example.net"},
		{UID: 90, Flagged: true, To: "=?UTF-8?Q?J=C3=B6rg_Brown?= <jb@example.net>"},
		{UID: 150},
	}
	c := fetchClient(t, box, "SELECT")
	for _, tc := range []struct{ query, seqs, uids string }{
		{"UNFLAGGED", "2 4", "40 150"},
		{"NOT UNFLAGGED", "1 3", "10 90"},
		{"FLAGGED UNFLAGGED", "", ""},
		{"DELETED", "", ""},
		{"UNDELETED", "1 2 3 4", "10 40 90 150"},
		{`TO "ANNE"`, "1", "10"},
		{`TO "example.net"`, "2 3", "40 90"},
		{`TO "brown <jb@"`, "3", "90"},
		{`TO "=?UTF-8?"`, "", ""},
		{`TO "anne" UNFLAGGED`, "", ""},
	} {
		t.Run("before/"+tc.query, func(t *testing.T) {
			requireSearch(t, c, "SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.seqs))
			requireSearch(t, c, "UID SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.uids))
		})
	}

	_, tagged := c.command("D1", `STORE 2,4 +FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "D1")
	for _, tc := range []struct{ query, seqs, uids string }{
		{"DELETED", "2 4", "40 150"},
		{"UNDELETED", "1 3", "10 90"},
		{"DELETED UNFLAGGED", "2 4", "40 150"},
		{"OR DELETED FLAGGED", "1 2 3 4", "10 40 90 150"},
		{`NOT DELETED TO "example"`, "1 3", "10 90"},
	} {
		t.Run("deleted/"+tc.query, func(t *testing.T) {
			requireSearch(t, c, "SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.seqs))
			requireSearch(t, c, "UID SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+tc.uids))
		})
	}

	_, tagged = c.command("D2", `STORE 2 -FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "D2")
	requireSearch(t, c, "SEARCH DELETED", "* SEARCH 4")
	requireSearch(t, c, "UID SEARCH UNDELETED", "* SEARCH 10 40 90")
	if len(box.fetches) != 0 {
		t.Fatalf("metadata searches read content: %v", box.fetches)
	}
}
