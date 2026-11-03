package rfctest

import (
	"fmt"
	"testing"
)

// TestWideSequenceDispatch proves that unsigned 32-bit sequence endpoints
// retain their range meaning on every Go architecture. Bounds beyond the
// snapshot are clipped before enumeration, never converted to a signed int.
// Run this proof on 386 as well as the host architecture.
//
// checklist: IMAP-SYNTAX-08
func TestWideSequenceDispatch(t *testing.T) {
	for _, set := range []string{"1:2147483648", "2147483648:1", "1:4294967295", "4294967295:1", "2:4294967295", "4294967295:2", "2147483648:4294967295", "4294967295"} {
		first, last := 1, 4
		switch set {
		case "2:4294967295", "4294967295:2":
			first = 2
		case "2147483648:4294967295", "4294967295":
			last = 0
		}
		for _, command := range []string{"FETCH", "STORE", "COPY"} {
			t.Run(command+"/"+set, func(t *testing.T) {
				if command == "COPY" {
					box := newCopyMailbox()
					c := expungeClient(t, box)
					lines, tagged := c.command("W1", "COPY %s Archive", set)
					requireOK(t, tagged, "W1")
					requireExactly(t, lines)
					var want []uint32
					for i := first; i <= last; i++ {
						want = append(want, box.messages[i-1].UID)
					}
					requireCopies(t, box, "Archive", want...)
					return
				}
				box := newStoreMailbox()
				c := storeClient(t, box)
				args := "UID"
				if command == "STORE" {
					args = `+FLAGS (\Draft)`
				}
				lines, tagged := c.command("W1", "%s %s %s", command, set, args)
				requireOK(t, tagged, "W1")
				var want []string
				for i := first; i <= last; i++ {
					flags := ""
					if i == 2 || i == 4 {
						flags = `\Seen`
					}
					if i == 4 {
						flags += ` \Flagged`
					}
					if command == "STORE" {
						if flags != "" {
							flags += " "
						}
						flags += `\Draft`
						want = append(want, fmt.Sprintf("* %d FETCH (FLAGS (%s))", i, flags))
					} else {
						want = append(want, fmt.Sprintf("* %d FETCH (UID %d FLAGS (%s))", i, box.messages[i-1].UID, flags))
					}
				}
				requireExactly(t, lines, want...)
				writes := 0
				for _, calls := range box.updates {
					writes += len(calls)
					for _, call := range calls {
						if call.Draft == nil || !*call.Draft {
							t.Fatalf("unexpected flag write: %#v", call)
						}
					}
				}
				wantWrites := 0
				if command == "STORE" && last >= first {
					wantWrites = last - first + 1
				}
				if writes != wantWrites {
					t.Fatalf("backend writes = %d, want %d", writes, wantWrites)
				}
			})
		}
	}
}
