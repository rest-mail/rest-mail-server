package rfctest

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// TestStoreInvalidSequenceSet checks RFC 3501 §§6.4.6, 6.4.8 and 9 through
// the public Session: invalid sets must not write flags, even in silent mode,
// select a valid prefix of a malformed list, or mutate session-only Deleted.
//
// checklist: IMAP-STORE-07
func TestStoreInvalidSequenceSet(t *testing.T) {
	for _, prefix := range []string{"STORE", "UID STORE"} {
		for _, empty := range []bool{false, true} {
			for _, action := range []string{"FLAGS", "+FLAGS", "-FLAGS", "FLAGS.SILENT", "+FLAGS.SILENT", "-FLAGS.SILENT"} {
				for _, set := range []string{
					"", "0", "00", "01", "+1", "-1", "1a", "abc", "**",
					"4294967296", "999999999999999999999999999999999999",
					"0:10", "1:0", "01:10", "1:010", "1:4294967296", "4294967296:1",
					",1", "1,", "1,,10", "1:", ":10", ":", "1:10:20",
					"*:0", "*:01", "1,*:10,", "1,\t10", "1\t:10", `"1"`, "(1)",
					"10,garbage", "4002,garbage", "4002,", "4002:garbage",
				} {
					t.Run(fmt.Sprintf("%s/empty=%t/%s/%q", prefix, empty, action, set), func(t *testing.T) {
						box := newStoreMailbox()
						if empty {
							box.messages = nil
						}
						c := storeClient(t, box)
						// Seed Deleted as well as persistent flags, so every action would
						// change something if an invalid set reached applyStore.
						_, tagged := c.command("P1", `STORE 2 +FLAGS.SILENT (\Deleted)`)
						requireOK(t, tagged, "P1")
						before, tagged := c.command("P2", "FETCH 1:* FLAGS")
						requireOK(t, tagged, "P2")
						clear(box.updates)
						lines, tagged := c.command("A1", `%s %s %s (\Flagged \Deleted)`, prefix, set, action)
						if !strings.HasPrefix(tagged, "A1 BAD ") || len(lines) != 0 {
							t.Errorf("invalid set: replies=%q tagged=%q, want only BAD", lines, tagged)
						}
						if len(box.updates) != 0 {
							t.Errorf("invalid set wrote backend flags: %+v", box.updates)
						}
						lines, tagged = c.command("N1", "NOOP")
						requireOK(t, tagged, "N1")
						requireExactly(t, lines)
						after, tagged := c.command("F1", "FETCH 1:* FLAGS")
						requireOK(t, tagged, "F1")
						if !slices.Equal(after, before) {
							t.Errorf("flags changed after invalid STORE: got %q, want %q", after, before)
						}
						// A rejected command must leave the selection writable and reusable.
						_, tagged = c.command("N2", `STORE 1 +FLAGS.SILENT (\Draft)`)
						requireOK(t, tagged, "N2")
						if !empty && len(box.updates[4001]) != 1 {
							t.Errorf("valid STORE after rejection: updates=%+v", box.updates)
						}
					})
				}
			}
		}
	}
}

// TestStoreValidSequenceSet preserves RFC 3501 §9's reversed ranges, star,
// lists and unsigned-32-bit boundary grammar, plus §6.4.8's sparse UID sets.
// It checks actual writes and wire replies rather than only completion status.
//
// checklist: IMAP-STORE-08
func TestStoreValidSequenceSet(t *testing.T) {
	for _, tc := range []struct {
		command string
		seqs    []int
	}{
		{"STORE 1", []int{1}},
		{"STORE *", []int{3}},
		{"STORE 3:1", []int{1, 2, 3}},
		{"STORE *:2", []int{2, 3}},
		{"STORE 1:4294967295", []int{1, 2, 3}},
		{"STORE 1,3", []int{1, 3}},
		{"UID STORE 4294967295", []int{3}},
		{"UID STORE *", []int{3}},
		{"UID STORE 40:10", []int{1, 2}},
		{"UID STORE *:40", []int{2, 3}},
		{"UID STORE 1:4294967295", []int{1, 2, 3}},
		{"UID STORE 10,4294967295", []int{1, 3}},
		{"UID STORE 11:39", nil},
		{"UID STORE 41", nil},
	} {
		for _, silent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/silent=%t", tc.command, silent), func(t *testing.T) {
				box := newStoreMailbox()
				box.messages = []imap.Message{{UID: 10}, {UID: 40}, {UID: 4294967295}}
				c := storeClient(t, box)
				action := "+FLAGS"
				if silent {
					action += ".SILENT"
				}
				lines, tagged := c.command("A1", `%s %s (\Flagged)`, tc.command, action)
				requireOK(t, tagged, "A1")
				var want []string
				for _, seq := range tc.seqs {
					uid := box.messages[seq-1].UID
					updates := box.updates[uid]
					if len(updates) != 1 || updates[0].Flagged == nil || !*updates[0].Flagged {
						t.Errorf("UID %d: updates=%+v, want one Flagged=true write", uid, updates)
					}
					if !silent {
						if strings.HasPrefix(tc.command, "UID ") {
							want = append(want, fmt.Sprintf(`* %d FETCH (UID %d FLAGS (\Flagged))`, seq, uid))
						} else {
							want = append(want, fmt.Sprintf(`* %d FETCH (FLAGS (\Flagged))`, seq))
						}
					}
				}
				if len(box.updates) != len(tc.seqs) {
					t.Errorf("backend updates=%+v, want only sequences %v", box.updates, tc.seqs)
				}
				requireExactly(t, lines, want...)
				lines, tagged = c.command("F1", "FETCH 1:* FLAGS")
				requireOK(t, tagged, "F1")
				want = nil
				for i, msg := range box.messages {
					flags := ""
					if slices.Contains(tc.seqs, i+1) {
						flags = `\Flagged`
					}
					want = append(want, fmt.Sprintf("* %d FETCH (UID %d FLAGS (%s))", i+1, msg.UID, flags))
				}
				requireExactly(t, lines, want...)
			})
		}
	}
	for _, command := range []string{"STORE *", "UID STORE *", "UID STORE 1:4294967295"} {
		t.Run("empty/"+command, func(t *testing.T) {
			box := newStoreMailbox()
			box.messages = nil
			c := storeClient(t, box)
			lines, tagged := c.command("A1", `%s +FLAGS (\Flagged)`, command)
			requireOK(t, tagged, "A1")
			requireExactly(t, lines)
			if len(box.updates) != 0 {
				t.Fatalf("empty mailbox wrote flags: %+v", box.updates)
			}
		})
	}
}
