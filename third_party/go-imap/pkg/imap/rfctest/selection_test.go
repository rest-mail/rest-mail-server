package rfctest

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// selectionMailbox exposes deterministic folder contents and records writes.
// Returning a copy keeps session-local flag changes out of the backend fixture.
type selectionMailbox struct {
	mailbox
	folders                map[string][]imap.Message
	stores, deletes, moves int
}

func (m *selectionMailbox) Messages(folder string) ([]imap.Message, error) {
	messages, ok := m.folders[folder]
	if !ok {
		return nil, errors.New("mailbox unavailable")
	}
	return slices.Clone(messages), nil
}
func (m *selectionMailbox) Store(uint32, imap.FlagUpdate) error { m.stores++; return nil }
func (m *selectionMailbox) Delete(uint32) error                 { m.deletes++; return nil }
func (m *selectionMailbox) Move(uint32, string) error           { m.moves++; return nil }

// exampleMailbox supplies the RFC examples' persistent UIDVALIDITY. Other
// UIDPLUS operations are not used by the selection tests.
type exampleMailbox struct{ *selectionMailbox }

func (*exampleMailbox) UIDValidity(string) (uint32, error) { return 3857529045, nil }
func (*exampleMailbox) AppendUID(string, imap.FlagUpdate, []byte) (uint32, error) {
	return 0, errors.New("unused")
}
func (*exampleMailbox) CopyUID(uint32, string) (uint32, error) { return 0, errors.New("unused") }

func selectionClient(t *testing.T, box imap.Mailbox) *client {
	t.Helper()
	c := dialMailbox(t, box)
	c.line()
	c.login()
	return c
}

func requireLine(t *testing.T, lines []string, want string) {
	t.Helper()
	if !slices.Contains(lines, want) {
		t.Fatalf("responses %q lack %q", lines, want)
	}
}

// TestSelectionExamples reproduces the mailbox counts, first unseen sequence
// and UID metadata from RFC 3501 §§6.3.1/6.3.2. FLAGS/PERMANENTFLAGS are allowed
// to vary by backend; the test checks this engine's advertised flag lists.
//
// checklist: IMAP-SELECT-01 IMAP-SELECT-02 IMAP-SELECT-03 IMAP-SELECT-04 IMAP-SELECT-05 IMAP-EXAMINE-01 IMAP-SELECT-13 IMAP-RESP-08
// checklist: IMAP2-CMD-75 IMAP2-CMD-78
func TestSelectionExamples(t *testing.T) {
	for _, tc := range []struct {
		command, folder, tag, mode string
		count, unseen, recent      int
	}{
		{"SELECT", "INBOX", "A142", "READ-WRITE", 172, 12, 1},
		{"EXAMINE", "blurdybloop", "A932", "READ-ONLY", 17, 8, 2},
	} {
		t.Run(tc.command, func(t *testing.T) {
			messages := make([]imap.Message, tc.count)
			for i := range messages {
				messages[i] = imap.Message{UID: uint32(4000 + i), Seen: true}
			}
			// The current engine approximates Recent as unseen. Arrange the fixture
			// to match the RFC count without claiming independent Recent semantics.
			messages[tc.unseen-1].Seen = false
			if tc.recent == 2 {
				messages[tc.unseen].Seen = false
			}
			messages[len(messages)-1].UID = 4391
			box := &exampleMailbox{&selectionMailbox{folders: map[string][]imap.Message{tc.folder: messages}}}
			c := selectionClient(t, box)
			lines, tagged := c.command(tc.tag, "%s %s", tc.command, tc.folder)
			if !strings.HasPrefix(tagged, tc.tag+" OK ["+tc.mode+"] ") {
				t.Fatalf("completion: %q", tagged)
			}
			for _, want := range []string{
				fmt.Sprintf("* %d EXISTS", tc.count),
				fmt.Sprintf("* %d RECENT", tc.recent),
				fmt.Sprintf("* OK [UNSEEN %d] Message %d is first unseen", tc.unseen, tc.unseen),
				"* OK [UIDVALIDITY 3857529045] UIDs valid", "* OK [UIDNEXT 4392] Predicted next UID",
				`* FLAGS (\Seen \Answered \Flagged \Deleted \Draft)`,
			} {
				requireLine(t, lines, want)
			}
			if tc.command == "EXAMINE" {
				requireLine(t, lines, "* OK [PERMANENTFLAGS ()] No permanent flags permitted")
			} else {
				requireLine(t, lines, `* OK [PERMANENTFLAGS (\Seen \Answered \Flagged \Deleted \Draft \*)] Limited`)
			}
			if box.stores+box.deletes+box.moves != 0 {
				t.Fatal("selection changed permanent state")
			}
		})
	}
}

// TestSelectionUnseen distinguishes a sequence number from an unread count,
// including empty and fully seen mailboxes where no UNSEEN code is valid.
//
// checklist: IMAP-SELECT-02 IMAP-RESP-08
func TestSelectionUnseen(t *testing.T) {
	for _, command := range []string{"SELECT", "EXAMINE"} {
		for _, tc := range []struct {
			name  string
			seen  []bool
			first int
		}{
			{"empty", nil, 0}, {"all-seen", []bool{true, true}, 0},
			{"first", []bool{false, false, false}, 1},
			{"middle", []bool{true, false, false}, 2},
			{"last", []bool{true, true, false}, 3},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				messages := make([]imap.Message, len(tc.seen))
				for i, seen := range tc.seen {
					messages[i] = imap.Message{UID: uint32(10 + i*10), Seen: seen}
				}
				c := selectionClient(t, &selectionMailbox{folders: map[string][]imap.Message{"INBOX": messages}})
				lines, tagged := c.command("s1", "%s INBOX", command)
				if !strings.HasPrefix(tagged, "s1 OK ") {
					t.Fatalf("selection: %q", tagged)
				}
				var unseen []string
				for _, line := range lines {
					if strings.HasPrefix(line, "* OK [UNSEEN ") {
						unseen = append(unseen, line)
					}
				}
				if tc.first == 0 {
					if len(unseen) != 0 {
						t.Fatalf("no unseen messages: %q", unseen)
					}
				} else if len(unseen) != 1 || unseen[0] != fmt.Sprintf("* OK [UNSEEN %d] Message %d is first unseen", tc.first, tc.first) {
					t.Fatalf("UNSEEN %q, want sequence %d", unseen, tc.first)
				}
			})
		}
	}
}

// TestSelectionFailureDropsOldMailbox proves the state change on backend
// failure, including that a later valid selection recovers normally.
//
// checklist: IMAP-SELECT-06
func TestSelectionFailureDropsOldMailbox(t *testing.T) {
	for _, initial := range []string{"SELECT", "EXAMINE"} {
		for _, command := range []string{"SELECT", "EXAMINE"} {
			t.Run(initial+"/"+command, func(t *testing.T) {
				box := &selectionMailbox{folders: map[string][]imap.Message{"INBOX": {{UID: 10}}}}
				c := selectionClient(t, box)
				if _, tagged := c.command("s1", "%s INBOX", initial); !strings.HasPrefix(tagged, "s1 OK ") {
					t.Fatal(tagged)
				}
				if _, tagged := c.command("s2", "%s unavailable", command); !strings.HasPrefix(tagged, "s2 NO ") {
					t.Fatalf("failed selection: %q", tagged)
				}
				for i, cmd := range []string{"FETCH 1 (FLAGS)", `STORE 1 +FLAGS (\Seen)`, "SEARCH ALL", "EXPUNGE"} {
					tag := fmt.Sprintf("f%d", i)
					lines, tagged := c.command(tag, "%s", cmd)
					if len(lines) != 0 || strings.HasPrefix(tagged, tag+" OK ") {
						t.Fatalf("%s after failure: %q %q", cmd, lines, tagged)
					}
				}
				if box.stores+box.deletes != 0 {
					t.Fatal("stale mailbox mutated")
				}
				if _, tagged := c.command("s3", "SELECT INBOX"); !strings.HasPrefix(tagged, "s3 OK ") {
					t.Fatal(tagged)
				}
				lines, tagged := c.command("s4", "FETCH 1 (UID)")
				if !strings.HasPrefix(tagged, "s4 OK ") || len(lines) != 1 || !strings.Contains(lines[0], "UID 10") {
					t.Fatalf("recovery: %q %q", lines, tagged)
				}
			})
		}
	}
}

// TestSelectionSwitchDoesNotExpunge proves replacing the selected mailbox
// drops old deletion marks even when the new mailbox contains the same UID.
//
// checklist: IMAP-SELECT-07
func TestSelectionSwitchDoesNotExpunge(t *testing.T) {
	for _, command := range []string{"SELECT", "EXAMINE"} {
		t.Run(command, func(t *testing.T) {
			box := &selectionMailbox{folders: map[string][]imap.Message{"INBOX": {{UID: 10}}, "other": {{UID: 10}, {UID: 20}}}}
			c := selectionClient(t, box)
			c.command("s1", "SELECT INBOX")
			if _, tagged := c.command("s2", `STORE 1 +FLAGS (\Deleted)`); !strings.HasPrefix(tagged, "s2 OK ") {
				t.Fatal(tagged)
			}
			lines, tagged := c.command("s3", "%s other", command)
			if !strings.HasPrefix(tagged, "s3 OK ") {
				t.Fatal(tagged)
			}
			requireLine(t, lines, "* 2 EXISTS")
			lines, tagged = c.command("s4", "FETCH 1 (FLAGS)")
			if !strings.HasPrefix(tagged, "s4 OK ") || len(lines) != 1 || strings.Contains(lines[0], `\Deleted`) {
				t.Fatalf("new selection: %q %q", lines, tagged)
			}
			lines, tagged = c.command("s5", "CLOSE")
			if !strings.HasPrefix(tagged, "s5 OK ") || len(lines) != 0 || box.deletes != 0 {
				t.Fatalf("CLOSE: %q %q, deletes=%d", lines, tagged, box.deletes)
			}
		})
	}
}

// TestExaminePreventsPermanentWrites covers explicit mutation commands and
// CLOSE, including UID variants, followed by a SELECT read-write control.
//
// checklist: IMAP-EXAMINE-02
func TestExaminePreventsPermanentWrites(t *testing.T) {
	box := &selectionMailbox{folders: map[string][]imap.Message{"INBOX": {{UID: 10}}}}
	c := selectionClient(t, box)
	if _, tagged := c.command("s1", "EXAMINE INBOX"); !strings.HasPrefix(tagged, "s1 OK [READ-ONLY] ") {
		t.Fatal(tagged)
	}
	for i, cmd := range []string{`STORE 1 +FLAGS (\Seen)`, `UID STORE 10 +FLAGS (\Seen)`, "EXPUNGE", "UID EXPUNGE 10", "MOVE 1 other", "UID MOVE 10 other"} {
		tag := fmt.Sprintf("w%d", i)
		lines, tagged := c.command(tag, "%s", cmd)
		if len(lines) != 0 || !strings.HasPrefix(tagged, tag+" NO ") {
			t.Fatalf("read-only %s: %q %q", cmd, lines, tagged)
		}
	}
	if _, tagged := c.command("s2", "CLOSE"); !strings.HasPrefix(tagged, "s2 OK ") {
		t.Fatal(tagged)
	}
	if box.stores+box.deletes+box.moves != 0 {
		t.Fatal("EXAMINE allowed a permanent write")
	}
	c.command("s3", "SELECT INBOX")
	if _, tagged := c.command("s4", `STORE 1 +FLAGS (\Seen)`); !strings.HasPrefix(tagged, "s4 OK ") || box.stores != 1 {
		t.Fatalf("SELECT did not restore writes: %q, stores=%d", tagged, box.stores)
	}
}

// TestExamineBodyFetchDoesNotSetSeen checks the implicit permanent write that
// non-peek FETCH normally performs, for both sequence and UID commands.
//
// checklist: IMAP-EXAMINE-03
func TestExamineBodyFetchDoesNotSetSeen(t *testing.T) {
	for _, selection := range []string{"EXAMINE", "SELECT"} {
		for _, command := range []string{"FETCH 1 (BODY[])", "UID FETCH 10 (BODY[])"} {
			t.Run(selection+"/"+command, func(t *testing.T) {
				box := &selectionMailbox{folders: map[string][]imap.Message{"INBOX": {{UID: 10}}}}
				c := selectionClient(t, box)
				if _, tagged := c.command("s1", "%s INBOX", selection); !strings.HasPrefix(tagged, "s1 OK ") {
					t.Fatal(tagged)
				}
				// The fixture body has multiple lines; consume the literal independently
				// of client.command, which accepts only line-oriented untagged replies.
				if _, err := fmt.Fprintf(c.conn, "f1 %s\r\n", command); err != nil {
					t.Fatal(err)
				}
				first := c.line()
				if !strings.Contains(first, "BODY[] {") {
					t.Fatalf("FETCH header: %q", first)
				}
				var size int
				if start := strings.LastIndex(first, "{"); start < 0 {
					t.Fatalf("missing literal size: %q", first)
				} else if _, err := fmt.Sscanf(first[start:], "{%d}", &size); err != nil {
					t.Fatal(err)
				}
				raw := make([]byte, size)
				if _, err := io.ReadFull(c.r, raw); err != nil {
					t.Fatal(err)
				}
				if string(raw) != "Subject: hello\r\n\r\nhi!\r\n" {
					t.Fatalf("body: %q", raw)
				}
				if line := c.line(); line != ")" {
					t.Fatalf("literal closing: %q", line)
				}
				if line := c.line(); !strings.HasPrefix(line, "f1 OK ") {
					t.Fatal(line)
				}
				wantStores := 0
				if selection == "SELECT" {
					wantStores = 1
				}
				if box.stores != wantStores {
					t.Fatalf("%s body fetch performed %d Store calls, want %d", selection, box.stores, wantStores)
				}
				lines, tagged := c.command("f2", "FETCH 1 (FLAGS)")
				if !strings.HasPrefix(tagged, "f2 OK ") || len(lines) != 1 || strings.Contains(lines[0], `\Seen`) != (selection == "SELECT") {
					t.Fatalf("%s session flags: %q %q", selection, lines, tagged)
				}
			})
		}
	}
}
