package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestFetchInvalidPartial proves the RFC 3501 §9 number.nz-number request
// grammar before any content access, response or Seen change. A response's
// <origin> is not a request range. Earlier valid body items must not dispatch
// when a later partial is invalid, even for empty or nonexistent UID results.
//
// checklist: IMAP-FETCH-18
func TestFetchInvalidPartial(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, target := range []string{"FETCH 1", "UID FETCH 10", "UID FETCH 999", "empty"} {
			for _, suffix := range []string{
				"<0>", "<1>", "<>", "<.1>", "<0.>", "<0.0>", "<0.00>", "<0.01>",
				"<+0.1>", "<-0.1>", "<-1.1>", "<0.+1>", "<0.-1>", "<0.-0>",
				"<4294967296.1>", "<0.4294967296>", "<999999999999999999999999.1>",
				"<0.999999999999999999999999>", "<x.1>", "<0.x>", "<0.1.2>",
				"<0.1", "0.1>", "<<0.1>>", "<0.1><0.1>", "<0.1>junk",
				"<0. 1>", "< 0.1>", "<0.1 >", "<0.\t1>", "<٠.1>", "<0.１>",
			} {
				for _, items := range []string{
					"BODY[]" + suffix,
					"(BODY.PEEK[TEXT]" + suffix + " FLAGS)",
					"(BODY[] BODY.PEEK[HEADER.FIELDS (SUBJECT)] BODY[TEXT]" + suffix + ")",
				} {
					t.Run(fmt.Sprintf("%s/%s/%s", selection, target, items), func(t *testing.T) {
						box := newFetchMailbox()
						prefix := target
						if target == "empty" {
							box.messages = nil
							prefix = "FETCH *"
						}
						c := fetchClient(t, box, selection)
						replies, tagged := fetchCommand(t, c, prefix+" "+items)
						if !strings.HasPrefix(tagged, "F1 BAD ") || len(replies) != 0 {
							t.Errorf("invalid partial: replies=%#v tagged=%q, want only BAD", replies, tagged)
						}
						if len(box.fetches) != 0 || len(box.updates) != 0 {
							t.Errorf("invalid partial accessed backend: fetches=%v updates=%v", box.fetches, box.updates)
						}
						lines, tagged := c.command("N1", "NOOP")
						requireOK(t, tagged, "N1")
						requireExactly(t, lines)
						lines, tagged = c.command("N2", "FETCH 1 FLAGS")
						requireOK(t, tagged, "N2")
						if target == "empty" {
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

// TestFetchPartialBounds checks both uint32 bounds without int overflow,
// decimal leading zeroes allowed by number (but not nz-number), case folding,
// truncation and origin labels. PEEK and EXAMINE never set Seen, even at EOF.
//
// checklist: IMAP-FETCH-19
func TestFetchPartialBounds(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
			for _, peek := range []bool{false, true} {
				for _, tc := range []struct{ suffix, origin, payload string }{
					{"<0.1>", "0", fetchText[:1]},
					{"<000.1>", "0", fetchText[:1]},
					{"<0003.1>", "3", fetchText[3:4]},
					{"<0.4294967295>", "0", fetchText},
					{"<1.4294967295>", "1", fetchText[1:]},
					{"<2147483648.1>", "2147483648", ""},
					{"<4294967295.4294967295>", "4294967295", ""},
					{"<0004294967295.1>", "4294967295", ""},
				} {
					t.Run(fmt.Sprintf("%s/%s/peek=%t/%s", selection, prefix, peek, tc.suffix), func(t *testing.T) {
						box := newFetchMailbox()
						c := fetchClient(t, box, selection)
						item := "bOdY"
						if peek {
							item += ".pEeK"
						}
						replies, tagged := fetchCommand(t, c, prefix+" (UID "+item+"[tExT]"+tc.suffix+" FLAGS)")
						requireOK(t, tagged, "F1")
						if len(replies) != 1 || !slices.Equal(replies[0].literals, []string{tc.payload}) {
							t.Fatalf("partial replies: %#v, want payload %q", replies, tc.payload)
						}
						label := fmt.Sprintf("BODY[TEXT]<%s> {%d}", tc.origin, len(tc.payload))
						if !strings.Contains(replies[0].framing, label) || strings.Contains(replies[0].framing, "PEEK") {
							t.Fatalf("framing %q, want %q", replies[0].framing, label)
						}
						wantFlags, writes := "FLAGS ()", 0
						if !peek && selection == "SELECT" {
							wantFlags, writes = `FLAGS (\Seen)`, 1
						}
						if !strings.Contains(replies[0].framing, wantFlags) || len(box.updates[10]) != writes {
							t.Fatalf("Seen: framing %q, updates %#v", replies[0].framing, box.updates)
						}
						if !slices.Equal(box.fetches, []uint32{10}) {
							t.Fatalf("Fetch calls %v, want [10]", box.fetches)
						}
					})
				}
			}
		}
	}
}
