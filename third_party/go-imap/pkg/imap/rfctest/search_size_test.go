package rfctest

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// TestSearchSizeKeys checks strict octet comparisons, not character counts or
// body-only sizes. Numeric operands follow RFC 3501's number grammar, which
// permits zero and leading zeroes (unlike nz-number used for sequence sets).
//
// checklist: IMAP-SEARCH-17
func TestSearchSizeKeys(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/empty=%t", selection, empty), func(t *testing.T) {
				box := newFetchMailbox()
				maxSize := uint64(4294967295)
				if strconv.IntSize == 32 {
					maxSize = 2147483647
				}
				box.messages = []imap.Message{
					{UID: 10, Size: 0},
					{UID: 40, Size: 99, Seen: true},
					{UID: 90, Size: 100, Answered: true},
					{UID: 150, Size: 101, Draft: true},
					{UID: 300, Size: 2147483647},
					{UID: 900, Size: int(maxSize)},
				}
				if empty {
					box.messages = nil
				}
				c := fetchClient(t, box, selection)
				for _, tc := range []struct{ query, seqs, uids string }{
					{"LARGER 100", "4 5 6", "150 300 900"},
					{"SMALLER 100", "1 2", "10 40"},
					{"LARGER 0", "2 3 4 5 6", "40 90 150 300 900"},
					{"SMALLER 0", "", ""},
					{"SMALLER 1", "1", "10"},
					{"lArGeR 000100", "4 5 6", "150 300 900"},
					{"sMaLlEr 000100", "1 2", "10 40"},
					{"SMALLER 0000000000000000000000000000000000000001", "1", "10"},
					{"LARGER 2147483647", "6", "900"},
					{"SMALLER 2147483648", "1 2 3 4 5", "10 40 90 150 300"},
					{"LARGER 4294967294", "6", "900"},
					{"LARGER 4294967295", "", ""},
					{"SMALLER 4294967295", "1 2 3 4 5", "10 40 90 150 300"},
					{"NOT LARGER 100", "1 2 3", "10 40 90"},
					{"OR SMALLER 100 LARGER 100", "1 2 4 5 6", "10 40 150 300 900"},
					{"(LARGER 99 SMALLER 101)", "3", "90"},
					{"LARGER 100 UID 40:300", "4 5", "150 300"},
					{"SMALLER 100 2:*", "2", "40"},
					{"LARGER 100 SMALLER 100", "", ""},
				} {
					t.Run(tc.query, func(t *testing.T) {
						seqs, uids := tc.seqs, tc.uids
						if strconv.IntSize == 32 {
							switch tc.query {
							case "LARGER 2147483647", "LARGER 4294967294":
								seqs, uids = "", ""
							case "SMALLER 2147483648", "SMALLER 4294967295":
								seqs, uids = "1 2 3 4 5 6", "10 40 90 150 300 900"
							}
						}
						if empty {
							seqs, uids = "", ""
						}
						requireSearch(t, c, "SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+seqs))
						requireSearch(t, c, "UID SEARCH "+tc.query, strings.TrimSpace("* SEARCH "+uids))
					})
				}
				lines, tagged := c.command("F1", "FETCH 1:* (FLAGS RFC822.SIZE)")
				requireOK(t, tagged, "F1")
				if empty {
					requireExactly(t, lines)
				} else {
					requireExactly(t, lines,
						`* 1 FETCH (UID 10 FLAGS () RFC822.SIZE 0)`,
						`* 2 FETCH (UID 40 FLAGS (\Seen) RFC822.SIZE 99)`,
						`* 3 FETCH (UID 90 FLAGS (\Answered) RFC822.SIZE 100)`,
						`* 4 FETCH (UID 150 FLAGS (\Draft) RFC822.SIZE 101)`,
						`* 5 FETCH (UID 300 FLAGS () RFC822.SIZE 2147483647)`,
						fmt.Sprintf("* 6 FETCH (UID 900 FLAGS () RFC822.SIZE %d)", maxSize))
				}
				if len(box.fetches) != 0 || len(box.updates) != 0 {
					t.Fatalf("metadata search accessed content or wrote flags: fetches=%v updates=%v", box.fetches, box.updates)
				}
			})
		}
	}
}

// TestSearchSizeOctets supplies a real encoded message with UTF-8 headers and
// quoted-printable body data. The host's full RFC822.SIZE is the same value
// FETCH reports; neither decoded body length nor Unicode rune count applies.
//
// checklist: IMAP-SEARCH-17
func TestSearchSizeOctets(t *testing.T) {
	raw := "Subject: café\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=C3=A9\r\n"
	box := newFetchMailbox()
	box.messages[0].Size = len(raw)
	box.raw[10] = raw
	c := fetchClient(t, box, "SELECT")
	for _, tc := range []struct {
		key  string
		size int
		want string
	}{
		{"LARGER", len(raw) - 1, "* SEARCH 10"},
		{"LARGER", len(raw), "* SEARCH"},
		{"SMALLER", len(raw) + 1, "* SEARCH 10"},
		{"SMALLER", len(raw), "* SEARCH"},
		{"LARGER", len([]rune(raw)), "* SEARCH 10"},
	} {
		requireSearch(t, c, fmt.Sprintf("UID SEARCH %s %d", tc.key, tc.size), tc.want)
	}
	if len(box.fetches) != 0 || len(box.updates) != 0 {
		t.Fatalf("size search accessed content or wrote flags: fetches=%v updates=%v", box.fetches, box.updates)
	}
}

// TestSearchInvalidSize rejects malformed/overflowing number operands before
// evaluating any criteria, even in an empty mailbox or behind a content key.
//
// checklist: IMAP-SEARCH-18
func TestSearchInvalidSize(t *testing.T) {
	for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
		for _, empty := range []bool{false, true} {
			box := newFetchMailbox()
			if empty {
				box.messages = nil
			}
			c := fetchClient(t, box, "SELECT")
			for _, key := range []string{"LARGER", "SMALLER"} {
				// A literal size is refused before its octets are read and ends the
				// session (literal_types_test.go), so this shared client has none.
				for _, operand := range []string{"", "+1", "-1", "1.0", "0x10", "1e2", "1_000", "1a", "*", "1:2", "4294967296", "999999999999999999999999999999999999", `"1"`, `""`, "１２", "1\t"} {
					for _, form := range []string{"%s", "NOT %s", "OR ALL %s", "(ALL %s)", `BODY "hello" %s`} {
						query := fmt.Sprintf(form, strings.TrimRight(key+" "+operand, " "))
						t.Run(fmt.Sprintf("%s/empty=%t/%s", prefix, empty, query), func(t *testing.T) {
							lines, tagged := c.command("Q1", "%s %s", prefix, query)
							if !strings.HasPrefix(tagged, "Q1 BAD ") || len(lines) != 0 {
								t.Errorf("invalid size: lines=%q tagged=%q, want only BAD", lines, tagged)
							}
							if len(box.fetches) != 0 || len(box.updates) != 0 {
								t.Fatalf("invalid search accessed backend: fetches=%v updates=%v", box.fetches, box.updates)
							}
							lines, tagged = c.command("N1", "NOOP")
							requireOK(t, tagged, "N1")
							requireExactly(t, lines)
						})
					}
				}
			}
			want := "* SEARCH"
			if !empty {
				if prefix == "SEARCH" {
					want += " 1"
				} else {
					want += " 10"
				}
			}
			requireSearch(t, c, prefix+" LARGER 0", want)
		}
	}
}
