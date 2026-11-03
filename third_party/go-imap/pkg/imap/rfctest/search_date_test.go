package rfctest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// TestSearchDateGrammar checks RFC 3501 §9 date-day, date-month and date-year,
// in both bare and quoted date forms, with all six calendar-date search keys.
// The message's internal and sent dates agree, so both key families have the
// same result; non-UTC times distinguish calendar dates from instants.
//
// checklist: IMAP-SEARCH-22
func TestSearchDateGrammar(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		box := newFetchMailbox()
		box.messages = []imap.Message{{UID: 40, Date: time.Date(2024, 2, 29, 23, 30, 0, 0, time.FixedZone("west", -5*60*60)), Seen: true}}
		box.raw = map[uint32]string{40: "Date: Thu, 29 Feb 2024 23:30:00 -0500\r\n\r\nbody\r\n"}
		c := fetchClient(t, box, selection)
		for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
			for _, key := range []string{"ON", "BEFORE", "SINCE", "SENTON", "SENTBEFORE", "SENTSINCE"} {
				for _, tc := range []struct {
					date     string
					relation int
				}{
					{"29-Feb-2024", 0}, {`"29-fEb-2024"`, 0},
					{"1-Mar-2024", -1}, {`"01-MAR-2024"`, -1},
					{"1-Jan-0000", 1}, {`"01-Jan-0001"`, 1},
					{"31-Dec-9999", -1}, {"29-Feb-2000", 1},
				} {
					t.Run(selection+"/"+prefix+"/"+key+"/"+tc.date, func(t *testing.T) {
						match := tc.relation == 0
						if strings.HasSuffix(key, "BEFORE") {
							match = tc.relation < 0
						} else if strings.HasSuffix(key, "SINCE") {
							match = tc.relation >= 0
						}
						want := "* SEARCH"
						if match {
							want += " 1"
							if prefix == "UID SEARCH" {
								want = "* SEARCH 40"
							}
						}
						requireSearch(t, c, prefix+" "+strings.ToLower(key)+" "+tc.date, want)
					})
				}
			}
		}
		if len(box.updates) != 0 {
			t.Fatalf("search changed flags: %v", box.updates)
		}
	}
}

// TestSearchInvalidDateGrammar rejects malformed date tokens before evaluating
// any criteria, even after BODY, inside Boolean/groups or in empty mailboxes.
// Each refusal must preserve command framing and the selected message flags.
//
// checklist: IMAP-SEARCH-23
func TestSearchInvalidDateGrammar(t *testing.T) {
	for _, prefix := range []string{"SEARCH", "UID SEARCH"} {
		for _, empty := range []bool{false, true} {
			box := newFetchMailbox()
			if empty {
				box.messages = nil
			}
			c := fetchClient(t, box, "SELECT")
			for _, key := range []string{"ON", "BEFORE", "SINCE", "SENTON", "SENTBEFORE", "SENTSINCE"} {
				for _, operand := range []string{
					`" 1-Jan-2025"`, `"1-Jan-2025 "`, "\"\t1-Jan-2025\"", "\"1-Jan-2025\t\"",
					"1-Jan-2025\t", "\t1-Jan-2025", "\"\u00a01-Jan-2025\"", "1-Jan-2025\u00a0",
					`"001-Jan-2025"`, "001-Jan-2025", "0-Jan-2025", "32-Jan-2025", "31-Apr-2025",
					"29-Feb-2025", "29-Feb-1900", "1-January-2025", "1-Foo-2025", "1-Jan-25",
					"1-Jan-10000", "+1-Jan-2025", "1-Jan-+025", `"1-Jan-2025`, `1-Jan-2025"`,
					`"1-Jan-2025\\"`, `"1-Jan-2025\""`, "１-Jan-2025", "1-Jaſ-2025", `""`,
				} {
					for _, form := range []string{"%s", "NOT %s", "OR ALL %s", "(ALL %s)", `BODY "hello" %s`} {
						query := fmt.Sprintf(form, key+" "+operand)
						t.Run(fmt.Sprintf("%s/empty=%t/%s", prefix, empty, query), func(t *testing.T) {
							lines, tagged := c.command("Q1", "%s %s", prefix, query)
							if !strings.HasPrefix(tagged, "Q1 BAD ") || len(lines) != 0 {
								t.Fatalf("invalid date: lines=%q tagged=%q, want only BAD", lines, tagged)
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
			want := "* SEARCH 1"
			if prefix == "UID SEARCH" {
				want = "* SEARCH 10"
			}
			if empty {
				want = "* SEARCH"
			}
			requireSearch(t, c, prefix+" ALL", want)
		}
	}
}
