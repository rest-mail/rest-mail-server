package rfctest

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestFetchInvalidItems checks the outer FETCH grammar before evaluating any
// message, including a valid body item followed by an invalid item. Rejection
// preserves the selection, flags and command stream even with no matching UID.
//
// checklist: IMAP-FETCH-20
func TestFetchInvalidItems(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, target := range []string{"FETCH 1", "UID FETCH 10", "UID FETCH 999", "empty"} {
			for _, items := range []string{
				"", "()", "( )", "(FLAGS", "FLAGS)", "((FLAGS))", "(FLAGS))", "(FLAGS) UID",
				"FLAGS UID", " FLAGS", "FLAGS ", "( FLAGS)", "(FLAGS )", "(FLAGS  UID)",
				"(FLAGS\tUID)", "(FLAGS\u00a0UID)", "(FLAGS)(UID)", "(FLAGS (UID))",
				"FAſT", "FLAGſ", "FAST FLAGS", "(FAST)", "(all)", "(FULL)", "(FLAGS FAST)", "(FULL FLAGS)",
				"(FAST ALL)", "(BODY[] FAST)", "BOGUS", "RFC822.FOO", "BODY.PEEK", "BODYSTRUCTUREX",
				"(BODY[] BOGUS)", "(BODY.PEEK[HEADER.FIELDS (SUBJECT)] FLAGS BOGUS)",
				"BODY[][]", "BODY[TEXT][HEADER]", "(BODY[TEXT)", "(BODY[TEXT]])", "(BODY[[TEXT]])", "(BODY[HEADER.FIELDS (SUBJECT])",
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
						t.Errorf("invalid items: replies=%#v tagged=%q, want only BAD", replies, tagged)
					}
					if len(box.fetches) != 0 || len(box.updates) != 0 {
						t.Errorf("invalid items accessed backend: fetches=%v updates=%v", box.fetches, box.updates)
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

// TestFetchItemForms compares macros with their RFC expansions, and singleton
// items with their list forms. This guards exact response octets, metadata,
// literals and Seen behavior, rather than merely accepting the request.
//
// checklist: IMAP-FETCH-21
func TestFetchItemForms(t *testing.T) {
	pairs := [][2]string{
		{"FAST", "(FLAGS INTERNALDATE RFC822.SIZE)"},
		{"all", "(FLAGS INTERNALDATE RFC822.SIZE ENVELOPE)"},
		{"FuLl", "(FLAGS INTERNALDATE RFC822.SIZE ENVELOPE BODY)"},
	}
	for _, item := range []string{
		"FLAGS", "uid", "INTERNALDATE", "ENVELOPE", "BODY", "BODYSTRUCTURE",
		"RFC822", "RFC822.HEADER", "RFC822.TEXT", "RFC822.SIZE", "BODY[]",
		"BODY.PEEK[TEXT]<0.4>", "BODY.PEEK[HEADER.FIELDS (DATE FROM)]",
		"BODY[HEADER.FIELDS.NOT (SUBJECT FROM)]",
	} {
		pairs = append(pairs, [2]string{item, "(" + item + ")"})
	}
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		for _, prefix := range []string{"FETCH 1", "UID FETCH 10"} {
			for _, pair := range pairs {
				t.Run(selection+"/"+prefix+"/"+pair[0], func(t *testing.T) {
					var results [2][]fetchReply
					boxes := [2]*fetchMailbox{newFetchMailbox(), newFetchMailbox()}
					for i, items := range pair {
						c := fetchClient(t, boxes[i], selection)
						replies, tagged := fetchCommand(t, c, prefix+" "+items)
						requireOK(t, tagged, "F1")
						if len(replies) != 1 {
							t.Fatalf("%s: got %d replies, want 1", items, len(replies))
						}
						results[i] = replies
					}
					if !reflect.DeepEqual(results[0], results[1]) ||
						!reflect.DeepEqual(boxes[0].fetches, boxes[1].fetches) ||
						!reflect.DeepEqual(boxes[0].updates, boxes[1].updates) {
						t.Fatalf("forms %q differ: responses=%#v, fetches=%v/%v, updates=%v/%v",
							pair, results, boxes[0].fetches, boxes[1].fetches, boxes[0].updates, boxes[1].updates)
					}
				})
			}
		}
	}
}
