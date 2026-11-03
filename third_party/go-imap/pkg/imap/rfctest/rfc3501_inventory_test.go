package rfctest

import (
	"fmt"
	"strings"
	"testing"
)

// checklist: IMAP-INV-STATE-02
func TestRFC3501RejectedNonSelectionKeepsState(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		t.Run(selection, func(t *testing.T) {
			box := newFetchMailbox()
			peer := fetchClient(t, box, selection)
			if selection == "SELECT" {
				_, tagged := peer.command("D1", `STORE 1 +FLAGS (\Deleted)`)
				requireOK(t, tagged, "D1")
			}
			priorWrites := len(box.updates[10])
			for number, command := range []string{"FROBNICATE", "CAPABILITY extra", "FETCH 1 FLAGS extra", "STORE 1 +FLAGS", "SEARCH NOT"} {
				tag := fmt.Sprintf("B%d", number)
				lines, tagged := peer.command(tag, "%s", command)
				requireExactly(t, lines)
				if !strings.HasPrefix(tagged, tag+" BAD ") && !strings.HasPrefix(tagged, tag+" NO ") {
					t.Fatalf("%s: %q, want rejection", command, tagged)
				}
				lines, tagged = peer.command("F1", "FETCH 1 (UID FLAGS)")
				requireOK(t, tagged, "F1")
				if len(lines) != 1 || !strings.Contains(lines[0], "UID 10") {
					t.Fatalf("lost selected mailbox after %s: %q", command, lines)
				}
				if strings.Contains(lines[0], `\Deleted`) != (selection == "SELECT") {
					t.Fatalf("changed deletion flag after %s: %q", command, lines)
				}
			}
			if len(box.fetches) != 0 || len(box.updates[10]) != priorWrites {
				t.Fatalf("unexpected content access or flag persistence: fetches=%v updates=%v", box.fetches, box.updates)
			}
		})
	}
}

// checklist: IMAP-INV-NAME-01
func TestRFC3501HierarchyDelimiter(t *testing.T) {
	peer := selectionClient(t, hierarchyMailbox{})
	for _, command := range []string{"LIST", "LSUB"} {
		lines, tagged := peer.command("L1", `%s "" "*"`, command)
		requireOK(t, tagged, "L1")
		if len(lines) != 3 {
			t.Fatalf("%s returned %d names, want 3", command, len(lines))
		}
		if names := strings.Join(listNames(t, command, lines), " "); names != "INBOX Work Work/Q1" {
			t.Fatalf("%s returned names %q, want INBOX Work Work/Q1", command, names)
		}
		for _, line := range lines {
			if !strings.Contains(line, ` "/" `) {
				t.Fatalf("inconsistent hierarchy delimiter: %q", line)
			}
		}
	}
}

// checklist: IMAP-EXPUNGE-07
func TestRFC3501DeletedSurvivesSessions(t *testing.T) {
	box := newStoreMailbox()
	first := storeClient(t, box)
	_, tagged := first.command("D1", `STORE 1 +FLAGS (\Deleted)`)
	requireOK(t, tagged, "D1")
	if !box.messages[0].Deleted {
		t.Fatal("Deleted was not supplied to the persistent backend")
	}
	second := storeClient(t, box)
	lines, tagged := second.command("F1", "FETCH 1 FLAGS")
	requireOK(t, tagged, "F1")
	if len(lines) != 1 || !strings.Contains(lines[0], `\Deleted`) {
		t.Fatalf("second session lost persistent Deleted: %q", lines)
	}
	_, tagged = second.command("D2", `STORE 1 -FLAGS (\Deleted)`)
	requireOK(t, tagged, "D2")
	lines, tagged = first.command("N1", "NOOP")
	requireOK(t, tagged, "N1")
	requireLine(t, lines, "* 1 FETCH (FLAGS ())")
	lines, tagged = first.command("F2", "FETCH 1 FLAGS")
	requireOK(t, tagged, "F2")
	if len(lines) != 1 || strings.Contains(lines[0], `\Deleted`) {
		t.Fatalf("first session retained the cleared Deleted flag: %q", lines)
	}
}
