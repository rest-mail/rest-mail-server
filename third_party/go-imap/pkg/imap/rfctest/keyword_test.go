package rfctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// keywordMessages has every system flag combination the keyword keys must
// ignore, with UIDs that differ from sequence numbers.
func keywordMessages() []imap.Message {
	return []imap.Message{
		{UID: 10, Seen: true, Flagged: true},
		{UID: 40, Answered: true, Draft: true},
		{UID: 90},
	}
}

// TestKeywordsAdvertisedAndStored proves keyword creation, persistence,
// case-insensitive search and removal match the advertised PERMANENTFLAGS.
//
// checklist: IMAP-STORE-06 IMAP-SEARCH-11 IMAP-INV-FLAGS-03
func TestKeywordsAdvertisedAndStored(t *testing.T) {
	box := newStoreMailbox()
	box.messages = keywordMessages()
	client := storeClient(t, box)
	lines, tagged := client.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	requireLine(t, lines, `* OK [PERMANENTFLAGS (\Seen \Answered \Flagged \Deleted \Draft \*)] Limited`)
	_, tagged = client.command("K1", `STORE 1 +FLAGS ($Forwarded Work)`)
	requireOK(t, tagged, "K1")
	_, tagged = client.command("S2", "SELECT INBOX")
	requireOK(t, tagged, "S2")
	lines, tagged = client.command("F1", "FETCH 1 FLAGS")
	requireOK(t, tagged, "F1")
	requireLine(t, lines, `* 1 FETCH (UID 10 FLAGS (\Seen \Flagged $Forwarded Work))`)
	requireSearch(t, client, "SEARCH KEYWORD work", "* SEARCH 1")
	_, tagged = client.command("K2", `STORE 1 -FLAGS ($forwarded)`)
	requireOK(t, tagged, "K2")
	lines, tagged = client.command("F2", "FETCH 1 FLAGS")
	requireOK(t, tagged, "F2")
	requireLine(t, lines, `* 1 FETCH (UID 10 FLAGS (\Seen \Flagged Work))`)
}

// TestSearchKeywordKeys covers RFC 3501 §6.4.4 KEYWORD and UNKEYWORD. No
// keywords are present in this fixture, so KEYWORD matches nothing and
// UNKEYWORD matches every message. The operand must be
// a flag-keyword atom; anything else is BAD and leaves the session usable.
//
// checklist: IMAP-SEARCH-11
func TestSearchKeywordKeys(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/empty=%t", selection, empty), func(t *testing.T) {
				box := newFetchMailbox()
				box.messages = keywordMessages()
				if empty {
					box.messages = nil
				}
				c := fetchClient(t, box, selection)
				if selection == "SELECT" && !empty {
					_, tagged := c.command("K1", `STORE 1:* +FLAGS (\Unsupported)`)
					requireOK(t, tagged, "K1")
				}
				for _, tc := range []struct{ query, seqs, uids string }{
					{"KEYWORD $Forwarded", "", ""},
					{"KEYWORD Work", "", ""},
					{"kEyWoRd work", "", ""},
					{"UNKEYWORD $Forwarded", "1 2 3", "10 40 90"},
					{"uNkEyWoRd Junk", "1 2 3", "10 40 90"},
					{"NOT KEYWORD Work", "1 2 3", "10 40 90"},
					{"NOT UNKEYWORD Work", "", ""},
					{"OR KEYWORD Work SEEN", "1", "10"},
					{"UNKEYWORD Work ANSWERED", "2", "40"},
					{"(UNKEYWORD a UNKEYWORD b) UID 40:*", "2 3", "40 90"},
					{"KEYWORD !#$&'+,-./:;<=>?@^_`|~", "", ""},
					{"UNKEYWORD !#$&'+,-./:;<=>?@^_`|~ 3", "3", "90"},
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
				for i, bad := range []string{
					"KEYWORD",
					"UNKEYWORD",
					`KEYWORD "Work"`,
					`KEYWORD \Seen`,
					`UNKEYWORD \Flagged`,
					"KEYWORD Wo]rk",
					"KEYWORD Wo*rk",
					"KEYWORD Wo%rk",
					"KEYWORD Wo{rk",
					"KEYWORD (Work)",
					"UNKEYWORD )",
					"KEYWORD caf\xc3\xa9",
				} {
					for _, prefix := range []string{"", "UID "} {
						tag := fmt.Sprintf("B%d", i)
						lines, tagged := c.command(tag, "%sSEARCH %s", prefix, bad)
						if !strings.HasPrefix(tagged, tag+" BAD") {
							t.Fatalf("%sSEARCH %s: %q, want BAD", prefix, bad, tagged)
						}
						requireExactly(t, lines)
					}
				}
				requireSearch(t, c, "SEARCH ALL", strings.TrimSpace("* SEARCH "+map[bool]string{false: "1 2 3", true: ""}[empty]))
				if len(box.fetches) != 0 {
					t.Fatalf("keyword search fetched content: %v", box.fetches)
				}
			})
		}
	}
}
