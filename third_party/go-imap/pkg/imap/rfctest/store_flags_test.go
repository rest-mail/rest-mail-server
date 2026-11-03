package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// storeActions are the six store-att-flags data-item names of RFC 3501 §9.
var storeActions = []string{"FLAGS", "+FLAGS", "-FLAGS", "FLAGS.SILENT", "+FLAGS.SILENT", "-FLAGS.SILENT"}

// TestStoreInvalidFlagList checks RFC 3501 §§6.4.6 and 9 through the public
// Session: the store-att-flags operand is a parenthesised flag-list or bare
// flags separated by single SP, and each flag is an atom with an optional
// leading "\". Anything else answers BAD before any flag changes. Every case
// names \Flagged and \Deleted, so a lenient parser would change message 2
// under every action.
//
// checklist: IMAP-STORE-09
func TestStoreInvalidFlagList(t *testing.T) {
	for _, target := range []string{"STORE 2", "UID STORE 4002"} {
		for _, action := range storeActions {
			for _, flags := range []string{
				`(\Flagged \Deleted`,
				`\Flagged \Deleted)`,
				`((\Flagged \Deleted))`,
				`(\Flagged) (\Deleted)`,
				`(\Flagged)(\Deleted)`,
				`(\Flagged) \Deleted`,
				`\Flagged (\Deleted)`,
				`(\Flagged  \Deleted)`,
				`( \Flagged \Deleted)`,
				`(\Flagged \Deleted )`,
				` (\Flagged \Deleted)`,
				`\Flagged  \Deleted`,
				"(\\Flagged\t\\Deleted)",
				`(\Flagged\Deleted)`,
				`(\\Flagged \Deleted)`,
				`(\Flagged \ \Deleted)`,
				`(\Flagged \* \Deleted)`,
				`(\Flagged k* \Deleted)`,
				`(\Flagged k% \Deleted)`,
				`(\Flagged k] \Deleted)`,
				`(\Flagged k{x \Deleted)`,
				`(\Flagged "kw" \Deleted)`,
				`(\Flagged "\Deleted")`,
				`(\Flagged k\w \Deleted)`,
				"(\\Flagged café \\Deleted)",
				"(\\Flagged k\x7fw \\Deleted)",
				"(\\Flagged k\x01w \\Deleted)",
			} {
				t.Run(fmt.Sprintf("%s/%s/%q", target, action, flags), func(t *testing.T) {
					box := newStoreMailbox()
					c := storeClient(t, box)
					// Seed Deleted on message 2 so -FLAGS would clear it and FLAGS
					// would keep it while clearing \Seen.
					_, tagged := c.command("P1", `STORE 2 +FLAGS.SILENT (\Deleted)`)
					requireOK(t, tagged, "P1")
					before, tagged := c.command("P2", "FETCH 1:* FLAGS")
					requireOK(t, tagged, "P2")
					clear(box.updates)
					lines, tagged := c.command("A1", "%s %s %s", target, action, flags)
					if !strings.HasPrefix(tagged, "A1 BAD ") || len(lines) != 0 {
						t.Errorf("invalid flags: replies=%q tagged=%q, want only BAD", lines, tagged)
					}
					if len(box.updates) != 0 {
						t.Errorf("invalid flags wrote backend flags: %+v", box.updates)
					}
					after, tagged := c.command("F1", "FETCH 1:* FLAGS")
					requireOK(t, tagged, "F1")
					if !slices.Equal(after, before) {
						t.Errorf("flags changed after invalid STORE: got %q, want %q", after, before)
					}
					// A rejected command must leave the selection writable.
					_, tagged = c.command("N1", `STORE 1 +FLAGS.SILENT (\Draft)`)
					requireOK(t, tagged, "N1")
					if len(box.updates[4001]) != 1 {
						t.Errorf("valid STORE after rejection: updates=%+v", box.updates)
					}
				})
			}
		}
	}
}

// TestStoreValidFlagList preserves the RFC 3501 §9 forms a client may send:
// an empty flag-list, bare flags, case-insensitive system flags, keywords and
// flag-extensions built from any ATOM-CHAR. It checks the resulting flags on
// the wire, not only the completion status.
//
// checklist: IMAP-STORE-10
func TestStoreValidFlagList(t *testing.T) {
	for _, target := range []string{"STORE 4", "UID STORE 4004"} {
		for _, tc := range []struct {
			action, flags, want string
		}{
			{"FLAGS", `()`, ``},
			{"FLAGS", `\Draft`, `\Draft`},
			{"FLAGS", `\Draft \Answered`, `\Answered \Draft`},
			{"FLAGS", `(\draft \ANSWERED)`, `\Answered \Draft`},
			{"+FLAGS", `\Answered`, `\Seen \Answered \Flagged`},
			{"+FLAGS", `($Label1 \Answered)`, `\Seen \Answered \Flagged $Label1`},
			{"+FLAGS", `(\X-Extension \Deleted)`, `\Seen \Flagged \Deleted`},
			{"+FLAGS", `(!#$&'+,-./:;<=>?@^_` + "`" + `|~ \Answered)`, `\Seen \Answered \Flagged !#$&'+,-./:;<=>?@^_` + "`" + `|~`},
			{"-FLAGS", `()`, `\Seen \Flagged`},
			{"-FLAGS", `\Seen`, `\Flagged`},
			{"-FLAGS", `(NonJunk \flagged \seen)`, ``},
		} {
			for _, silent := range []bool{false, true} {
				action := tc.action
				if silent {
					action += ".SILENT"
				}
				t.Run(fmt.Sprintf("%s/%s/%q", target, action, tc.flags), func(t *testing.T) {
					box := newStoreMailbox()
					c := storeClient(t, box)
					lines, tagged := c.command("A1", "%s %s %s", target, action, tc.flags)
					requireOK(t, tagged, "A1")
					if silent {
						requireExactly(t, lines)
					} else if strings.HasPrefix(target, "UID ") {
						requireExactly(t, lines, fmt.Sprintf("* 4 FETCH (UID 4004 FLAGS (%s))", tc.want))
					} else {
						requireExactly(t, lines, fmt.Sprintf("* 4 FETCH (FLAGS (%s))", tc.want))
					}
					lines, tagged = c.command("F1", "FETCH 4 FLAGS")
					requireOK(t, tagged, "F1")
					requireExactly(t, lines, fmt.Sprintf("* 4 FETCH (UID 4004 FLAGS (%s))", tc.want))
				})
			}
		}
	}
}
