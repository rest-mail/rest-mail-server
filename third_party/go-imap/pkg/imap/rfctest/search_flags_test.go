package rfctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// TestSearchAnsweredDraftKeys exercises RFC 3501 §6.4.4's four flag keys
// against every Answered/Draft combination. Seen and Flagged deliberately
// disagree with those flags, and UIDs differ from sequence numbers.
//
// checklist: IMAP-SEARCH-15
// checklist: IMAP2-SEC-216
func TestSearchAnsweredDraftKeys(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/empty=%t", selection, empty), func(t *testing.T) {
				box := newFetchMailbox()
				box.messages = []imap.Message{
					{UID: 10, Answered: true, Flagged: true},
					{UID: 40, Draft: true, Seen: true},
					{UID: 90, Answered: true, Draft: true},
					{UID: 150, Seen: true, Flagged: true},
				}
				if empty {
					box.messages = nil
				}
				c := fetchClient(t, box, selection)
				for _, tc := range []struct{ query, seqs, uids string }{
					{"ANSWERED", "1 3", "10 90"},
					{"UNANSWERED", "2 4", "40 150"},
					{"DRAFT", "2 3", "40 90"},
					{"UNDRAFT", "1 4", "10 150"},
					{"aNsWeReD", "1 3", "10 90"},
					{"uNaNsWeReD", "2 4", "40 150"},
					{"dRaFt", "2 3", "40 90"},
					{"uNdRaFt", "1 4", "10 150"},
					{"ANSWERED DRAFT", "3", "90"},
					{"(UNANSWERED UNDRAFT)", "4", "150"},
					{"OR ANSWERED DRAFT", "1 2 3", "10 40 90"},
					{"NOT OR ANSWERED DRAFT", "4", "150"},
					{"OR (ANSWERED UNDRAFT) (UNANSWERED DRAFT)", "1 2", "10 40"},
					{"NOT UNANSWERED", "1 3", "10 90"},
					{"NOT UNDRAFT", "2 3", "40 90"},
					{"ANSWERED UID 40:90", "3", "90"},
					{"UNDRAFT 2:4", "4", "150"},
					{"ANSWERED UNANSWERED", "", ""},
					{"DRAFT UNDRAFT", "", ""},
				} {
					t.Run(tc.query, func(t *testing.T) {
						seqs, uids := tc.seqs, tc.uids
						if empty {
							seqs, uids = "", ""
						}
						requireSearch(t, c, "SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+seqs))
						requireSearch(t, c, "UID SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+uids))
					})
				}
				if len(box.fetches) != 0 || len(box.updates) != 0 {
					t.Fatalf("metadata search accessed content or changed flags: fetches=%v updates=%v", box.fetches, box.updates)
				}
				lines, tagged := c.command("F1", "FETCH 1:* FLAGS")
				requireOK(t, tagged, "F1")
				if empty {
					requireExactly(t, lines)
				} else {
					requireExactly(t, lines,
						`* 1 FETCH (UID 10 FLAGS (\Answered \Flagged))`,
						`* 2 FETCH (UID 40 FLAGS (\Seen \Draft))`,
						`* 3 FETCH (UID 90 FLAGS (\Answered \Draft))`,
						`* 4 FETCH (UID 150 FLAGS (\Seen \Flagged))`)
				}
			})
		}
	}
}

// TestSearchAnsweredDraftStore checks live selected-session flags after add,
// removal, replacement and a refused backend update, without reselecting.
//
// checklist: IMAP-SEARCH-16
func TestSearchAnsweredDraftStore(t *testing.T) {
	box := newFetchMailbox()
	c := fetchClient(t, box, "SELECT")
	for _, tc := range []struct{ command, answered, draft string }{
		{`STORE 1 +FLAGS.SILENT (\Answered)`, "1", ""},
		{`UID STORE 10 +FLAGS.SILENT (\Draft)`, "1", "1"},
		{`STORE 1 -FLAGS.SILENT (\Answered)`, "", "1"},
		{`UID STORE 10 FLAGS.SILENT (\Answered)`, "1", ""},
		{`STORE 1 FLAGS.SILENT ()`, "", ""},
	} {
		t.Run(tc.command, func(t *testing.T) {
			lines, tagged := c.command("U1", "%s", tc.command)
			requireOK(t, tagged, "U1")
			requireExactly(t, lines)
			for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
				answered, draft := tc.answered, tc.draft
				if prefix == "UID SEARCH" {
					if answered != "" {
						answered = "10"
					}
					if draft != "" {
						draft = "10"
					}
				}
				requireSearch(t, c, prefix+" ANSWERED", strings.TrimSpace("* SEARCH "+answered))
				requireSearch(t, c, prefix+" DRAFT", strings.TrimSpace("* SEARCH "+draft))
			}
		})
	}
	box.fail = true
	_, tagged := c.command("U2", `STORE 1 +FLAGS.SILENT (\Answered \Draft)`)
	if !strings.HasPrefix(tagged, "U2 NO ") {
		t.Fatalf("refused STORE: %q", tagged)
	}
	for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
		requireSearch(t, c, prefix+" OR ANSWERED DRAFT", "* SEARCH")
		want := "* SEARCH 1"
		if prefix == "UID SEARCH" {
			want = "* SEARCH 10"
		}
		requireSearch(t, c, prefix+" UNANSWERED UNDRAFT", want)
	}
	if len(box.fetches) != 0 || len(box.updates[10]) != 5 {
		t.Fatalf("unexpected backend calls: fetches=%v updates=%v", box.fetches, box.updates)
	}
}
