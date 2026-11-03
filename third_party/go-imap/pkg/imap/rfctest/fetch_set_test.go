package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// TestFetchInvalidSequenceSet rejects the RFC 3501 §9 grammar's near misses
// before loading content or marking Seen, including in an empty mailbox.
//
// checklist: IMAP-FETCH-16
func TestFetchInvalidSequenceSet(t *testing.T) {
	for _, prefix := range []string{"FETCH", "UID FETCH"} {
		for _, empty := range []bool{false, true} {
			for _, set := range []string{
				"", "0", "00", "01", "+1", "-1", "1a", "abc", "**",
				"4294967296", "999999999999999999999999999999999999",
				"0:10", "1:0", "01:10", "1:010", "1:4294967296", "4294967296:1",
				",1", "1,", "1,,10", "1:", ":10", ":", "1:10:20",
				"*:0", "*:01", "1,*:10,", "1,\t10", "1\t:10", `"1"`, "(1)",
			} {
				t.Run(fmt.Sprintf("%s/empty=%t/%q", prefix, empty, set), func(t *testing.T) {
					box := newFetchMailbox()
					if empty {
						box.messages = nil
					}
					c := fetchClient(t, box, "SELECT")
					replies, tagged := fetchCommand(t, c, prefix+" "+set+" BODY[]")
					if !strings.HasPrefix(tagged, "F1 BAD ") || len(replies) != 0 {
						t.Errorf("invalid set: replies=%#v tagged=%q, want only BAD", replies, tagged)
					}
					if len(box.fetches) != 0 || len(box.updates) != 0 {
						t.Errorf("invalid set accessed backend: fetches=%v updates=%v", box.fetches, box.updates)
					}
					lines, tagged := c.command("N1", "NOOP")
					requireOK(t, tagged, "N1")
					requireExactly(t, lines)
					lines, tagged = c.command("N2", "FETCH 1 FLAGS")
					requireOK(t, tagged, "N2")
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

// TestFetchValidSequenceSet keeps reversed ranges, star and 32-bit bounds
// working; sparse/nonexistent UIDs are resolved by mailbox membership, not by
// expanding every integer in the range (RFC 3501 §§6.4.8, 9).
//
// checklist: IMAP-FETCH-17
func TestFetchValidSequenceSet(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, tc := range []struct {
			command string
			seqs    []int
		}{
			{"FETCH 1 FLAGS", []int{1}},
			{"FETCH * FLAGS", []int{3}},
			{"FETCH 3:1 FLAGS", []int{1, 2, 3}},
			{"FETCH *:2 FLAGS", []int{2, 3}},
			{"FETCH 1:4294967295 FLAGS", []int{1, 2, 3}},
			{"FETCH 1,3 FLAGS", []int{1, 3}},
			{"UID FETCH 4294967295 FLAGS", []int{3}},
			{"UID FETCH * FLAGS", []int{3}},
			{"UID FETCH 40:10 FLAGS", []int{1, 2}},
			{"UID FETCH *:40 FLAGS", []int{2, 3}},
			{"UID FETCH 1:4294967295 FLAGS", []int{1, 2, 3}},
			{"UID FETCH 10,4294967295 FLAGS", []int{1, 3}},
			{"UID FETCH 11:39 FLAGS", nil},
			{"UID FETCH 41 FLAGS", nil},
		} {
			t.Run(selection+"/"+tc.command, func(t *testing.T) {
				box := newFetchMailbox()
				box.messages = append(box.messages, imap.Message{UID: 40}, imap.Message{UID: 4294967295})
				c := fetchClient(t, box, selection)
				lines, tagged := c.command("F1", "%s", tc.command)
				requireOK(t, tagged, "F1")
				var want []string
				for _, seq := range tc.seqs {
					want = append(want, fmt.Sprintf("* %d FETCH (UID %d FLAGS ())", seq, box.messages[seq-1].UID))
				}
				if !slices.Equal(lines, want) {
					t.Fatalf("FETCH replies=%q, want %q", lines, want)
				}
				if len(box.fetches) != 0 || len(box.updates) != 0 {
					t.Fatalf("metadata-only fetch accessed backend: fetches=%v updates=%v", box.fetches, box.updates)
				}
			})
		}
	}
	for _, command := range []string{"FETCH * FLAGS", "UID FETCH * FLAGS", "UID FETCH 1:4294967295 FLAGS"} {
		t.Run("empty/"+command, func(t *testing.T) {
			box := newFetchMailbox()
			box.messages = nil
			c := fetchClient(t, box, "SELECT")
			lines, tagged := c.command("F1", "%s", command)
			requireOK(t, tagged, "F1")
			requireExactly(t, lines)
		})
	}
}
