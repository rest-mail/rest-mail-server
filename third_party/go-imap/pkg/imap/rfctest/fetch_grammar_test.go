package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestFetchComposedGrammar mixes the outer item list, numeric MIME paths,
// quoted header-name astrings and partials in a single command. The literals
// and their boundaries must agree with the source message, not just parse.
//
// checklist: IMAP-FETCH-15
func TestFetchComposedGrammar(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, command := range []string{"FETCH 1", "UID FETCH 10"} {
			t.Run(selection+"/"+command, func(t *testing.T) {
				box := newFetchMailbox()
				box.raw[10] = sectionRaw
				box.messages[0].Size = len(sectionRaw)
				c := fetchClient(t, box, selection)
				replies, tagged := fetchCommand(t, c, command+` (BODY.PEEK[1]<1.4> BODY.PEEK[3.HEADER.FIELDS ("Subject")] BODY.PEEK[4.2.2.1]<0.3> RFC822.SIZE)`)
				requireOK(t, tagged, "F1")
				const header = "Subject: inner three\r\n\r\n"
				want := fmt.Sprintf(`* 1 FETCH (UID 10 FLAGS () RFC822.SIZE %d BODY[1]<1> {4} BODY[3.HEADER.FIELDS ("Subject")] {%d} BODY[4.2.2.1]<0> {3})`, len(sectionRaw), len(header))
				if len(replies) != 1 || replies[0].framing != want || !slices.Equal(replies[0].literals, []string{"art ", header, "alt"}) {
					t.Fatalf("replies %#v, want framing %q and exact source literals", replies, want)
				}
				if !slices.Equal(box.fetches, []uint32{10}) || len(box.updates) != 0 {
					t.Fatalf("backend effects: %v %v", box.fetches, box.updates)
				}
				lines, tagged := c.command("N1", "NOOP")
				requireOK(t, tagged, "N1")
				requireExactly(t, lines)
			})
		}
	}
}

// TestFetchComposedGrammarRefusals places invalid names/macros, section paths,
// header lists and partials before and after valid body items. Validation must
// finish before any message is read, marked Seen or returned, including for
// empty selections and nonexistent UIDs. A later valid FETCH proves recovery.
//
// checklist: IMAP-FETCH-15
func TestFetchComposedGrammarRefusals(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, target := range []string{"FETCH 1", "UID FETCH 10", "UID FETCH 999", "empty"} {
			for _, invalid := range []string{
				"FULL", "RFC822.UNKNOWN", "BODY.PEEK", "BODY[1.MIME.HEADER]",
				"BODY[1.0]", "BODY[4294967296.MIME]", "BODY[3.HEADER.FIELDS ()]",
				`BODY[3.HEADER.FIELDS ("Subject""From")]`,
				"BODY[3.HEADER.FIELDS (Subject)]<0.0>",
				"BODY[4.2.2.1]<1>", "BODY[4.2.2.1]<0.4294967296>",
				"BODY.PEEK[1]<0.4>junk", "BODY[1][]", "BODY[1]<0.4><0.1>",
			} {
				for _, items := range []string{
					"(" + invalid + " BODY[])",
					"(BODY[] " + invalid + " BODY.PEEK[3.HEADER.FIELDS (Subject)]<0.8>)",
				} {
					t.Run(selection+"/"+target+"/"+items, func(t *testing.T) {
						box := newFetchMailbox()
						box.raw[10] = sectionRaw
						command := target
						if target == "empty" {
							box.messages = nil
							command = "FETCH *"
						}
						c := fetchClient(t, box, selection)
						replies, tagged := fetchCommand(t, c, command+" "+items)
						if len(replies) != 0 || !strings.HasPrefix(tagged, "F1 BAD ") {
							t.Fatalf("invalid items: %#v %q", replies, tagged)
						}
						if len(box.fetches) != 0 || len(box.updates) != 0 {
							t.Fatalf("backend effects: %v %v", box.fetches, box.updates)
						}
						lines, tagged := c.command("N1", "FETCH 1 FLAGS")
						requireOK(t, tagged, "N1")
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
