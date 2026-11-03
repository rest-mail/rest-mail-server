package rfctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// TestSearchInvalidSequenceSet checks synthetic RFC 3501 §9 grammar near misses
// as bare keys and UID operands, including under Boolean/group operators. The
// entire command must fail before evaluation, even after a content-search key.
//
// checklist: IMAP-SEARCH-13
func TestSearchInvalidSequenceSet(t *testing.T) {
	for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
		for _, empty := range []bool{false, true} {
			for _, set := range []string{
				"0", "00", "01", "+1", "-1", "1a", "abc", "**",
				"4294967296", "999999999999999999999999999999999999",
				"0:10", "1:0", "01:10", "1:010", "1:4294967296", "4294967296:1",
				",1", "1,", "1,,10", "1:", ":10", ":", "1:10:20",
				"*:0", "*:01", "1,*:10,", "1,\t10", "1\t:10", `"1"`, `""`,
			} {
				for _, form := range []string{"%s", "UID %s", "NOT %s", "NOT UID %s", "OR ALL %s", "OR ALL UID %s", "(ALL %s)", "(ALL UID %s)", `BODY "hello" %s`, `BODY "hello" UID %s`} {
					query := fmt.Sprintf(form, set)
					t.Run(fmt.Sprintf("%s/empty=%t/%s", prefix, empty, query), func(t *testing.T) {
						box := newFetchMailbox()
						if empty {
							box.messages = nil
						}
						c := fetchClient(t, box, "SELECT")
						lines, tagged := c.command("Q1", "%s %s", prefix, query)
						if !strings.HasPrefix(tagged, "Q1 BAD ") || len(lines) != 0 {
							t.Errorf("malformed set: lines=%q tagged=%q, want only BAD", lines, tagged)
						}
						if len(box.fetches) != 0 || len(box.updates) != 0 {
							t.Errorf("rejected query evaluated content or wrote flags: fetches=%v updates=%v", box.fetches, box.updates)
						}
						lines, tagged = c.command("N1", "NOOP")
						requireOK(t, tagged, "N1")
						requireExactly(t, lines)
						want := "* SEARCH"
						if !empty {
							if prefix == "SEARCH" {
								want += " 1"
							} else {
								want += " 10"
							}
						}
						requireSearch(t, c, prefix+" ALL", want)
						lines, tagged = c.command("F1", "FETCH 1 FLAGS")
						requireOK(t, tagged, "F1")
						if empty {
							requireExactly(t, lines)
						} else {
							requireExactly(t, lines, "* 1 FETCH (UID 10 FLAGS ())")
						}
					})
				}
			}
		}
	}
}

// TestSearchValidSequenceSet proves valid boundaries and overlapping sets keep
// membership semantics: bare keys use sequence numbers even under UID SEARCH,
// UID keys use sparse UIDs, and every matching message is returned only once.
//
// checklist: IMAP-SEARCH-14
func TestSearchValidSequenceSet(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, empty := range []bool{false, true} {
			box := newFetchMailbox()
			box.messages = append(box.messages, imap.Message{UID: 40}, imap.Message{UID: 4294967295})
			if empty {
				box.messages = nil
			}
			c := fetchClient(t, box, selection)
			for _, tc := range []struct{ query, seqs, uids string }{
				{"1", "1", "10"}, {"*", "3", "4294967295"},
				{"3:1", "1 2 3", "10 40 4294967295"}, {"*:2", "2 3", "40 4294967295"},
				{"1:4294967295", "1 2 3", "10 40 4294967295"}, {"4294967295", "", ""},
				{"1,3:1,2,*", "1 2 3", "10 40 4294967295"},
				{"UID 4294967295", "3", "4294967295"}, {"UID *", "3", "4294967295"},
				{"UID 40:10", "1 2", "10 40"}, {"UID *:40", "2 3", "40 4294967295"},
				{"UID 1:4294967295", "1 2 3", "10 40 4294967295"},
				{"UID 10,40:10,40,*", "1 2 3", "10 40 4294967295"},
				{"UID 11:39", "", ""}, {"UID 41", "", ""},
				{"(UID 40:10 2:*)", "2", "40"},
				{"OR 1 UID 4294967295", "1 3", "10 4294967295"},
				{"NOT UID 40", "1 3", "10 4294967295"},
			} {
				t.Run(fmt.Sprintf("%s/empty=%t/%s", selection, empty, tc.query), func(t *testing.T) {
					for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
						matches := tc.seqs
						if prefix == "UID SEARCH" {
							matches = tc.uids
						}
						if empty {
							matches = ""
						}
						requireSearch(t, c, prefix+" "+tc.query, strings.TrimSpace("* SEARCH "+matches))
					}
				})
			}
			if len(box.fetches) != 0 || len(box.updates) != 0 {
				t.Fatalf("set-only search accessed backend: fetches=%v updates=%v", box.fetches, box.updates)
			}
		}
	}
}
