package rfctest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/server"
)

// TestStoreLeavesRecentAlone: STORE with FLAGS replaces the flags "other than
// \Recent" (RFC 3501 §6.4.6), and \Recent is not the client's to set or clear
// (§2.3.2). Naming it in a FLAGS, +FLAGS or -FLAGS list is refused and changes
// no flag (TestStoreRefusesRecent has the full matrix), and it never appears in
// the flags a STORE answers with. This test used to accept and ignore such a
// list, which the §9 flag production does not allow.
//
// checklist: IMAP-STORE-02 IMAP-STORE-11
func TestStoreLeavesRecentAlone(t *testing.T) {
	box := newStoreMailbox()
	c := storeClient(t, box)

	untagged, tagged := c.command("A0", `STORE 1 FLAGS (\Seen)`)
	requireOK(t, tagged, "A0")
	requireExactly(t, untagged, `* 1 FETCH (FLAGS (\Seen))`)

	for i, command := range []string{`STORE 1 FLAGS (\Recent \Seen)`, `STORE 1 -FLAGS (\Recent)`, `STORE 1 +FLAGS (\Recent)`} {
		tag := fmt.Sprintf("A%d", i+1)
		untagged, tagged = c.command(tag, "%s", command)
		requireExactly(t, untagged)
		if !strings.HasPrefix(tagged, tag+" BAD ") {
			t.Fatalf("%s: %q, want BAD", command, tagged)
		}
	}
	untagged, tagged = c.command("F1", "FETCH 1 FLAGS")
	requireOK(t, tagged, "F1")
	requireExactly(t, untagged, `* 1 FETCH (UID 4001 FLAGS (\Seen))`)

	untagged, tagged = c.command("A4", `STORE 1 FLAGS ()`)
	requireOK(t, tagged, "A4")
	requireExactly(t, untagged, `* 1 FETCH (FLAGS ())`)
}

// TestStoreRefusesRecent: the flag production "Does not include \Recent" (RFC
// 3501 §9), and "\Recent can not be used as an argument in a STORE or APPEND
// command" (§2.3.2), so naming it is a syntax error and gets BAD. The whole
// command is refused: no listed flag is written, even one that is legal. A
// keyword merely spelled Recent has no backslash and stays an ordinary flag.
//
// checklist: IMAP-STORE-11
func TestStoreRefusesRecent(t *testing.T) {
	for _, prefix := range []struct{ command, set string }{{"STORE", "1"}, {"UID STORE", "4001"}} {
		for _, action := range []string{"FLAGS", "+FLAGS", "-FLAGS", "FLAGS.SILENT", "+FLAGS.SILENT", "-FLAGS.SILENT"} {
			for _, operand := range []string{
				`(\Recent)`, `(\RECENT)`, `(\recent)`, `(\Seen \Recent)`, `(\Recent \Seen)`,
				`\Recent`, `\Seen \Recent`,
			} {
				t.Run(prefix.command+"/"+action+"/"+operand, func(t *testing.T) {
					box := newStoreMailbox()
					c := storeClient(t, box)
					lines, tagged := c.command("A1", "%s %s %s %s", prefix.command, prefix.set, action, operand)
					requireExactly(t, lines)
					if !strings.HasPrefix(tagged, "A1 BAD ") {
						t.Fatalf("STORE naming \\Recent: %q, want BAD", tagged)
					}
					if len(box.updates) != 0 {
						t.Fatalf("refused STORE wrote flags: %v", box.updates)
					}
					lines, tagged = c.command("F1", "FETCH 1 FLAGS")
					requireOK(t, tagged, "F1")
					requireExactly(t, lines, "* 1 FETCH (UID 4001 FLAGS ())")
				})
			}
		}
	}

	box := newStoreMailbox()
	c := storeClient(t, box)
	lines, tagged := c.command("K1", "STORE 1 +FLAGS (Recent)")
	requireOK(t, tagged, "K1")
	requireExactly(t, lines, "* 1 FETCH (FLAGS (Recent))")
}

// sharedMailbox is an INBOX another source can change while a session has it
// selected: its flag writes are applied, and set changes a message as another
// client's STORE would.
type sharedMailbox struct {
	mailbox
	mu       sync.Mutex
	messages []imap.Message
}

func (m *sharedMailbox) Messages(folder string) ([]imap.Message, error) {
	if folder != "INBOX" {
		return nil, errors.New("mailbox unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.messages), nil
}

func (m *sharedMailbox) Store(uid uint32, update imap.FlagUpdate) error {
	m.set(uid, func(msg *imap.Message) {
		for _, f := range []struct {
			to   *bool
			from *bool
		}{{&msg.Seen, update.Seen}, {&msg.Answered, update.Answered}, {&msg.Flagged, update.Flagged}, {&msg.Draft, update.Draft}} {
			if f.from != nil {
				*f.to = *f.from
			}
		}
	})
	return nil
}

func (m *sharedMailbox) set(uid uint32, change func(*imap.Message)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.messages {
		if m.messages[i].UID == uid {
			change(&m.messages[i])
		}
	}
}

// TestStoreSilentStillReportsOtherSourcesChanges: .SILENT suppresses only the
// STORE's own answer. "Regardless of whether or not the .SILENT suffix was
// used, the server SHOULD send an untagged FETCH response if a change to a
// message's flags from an external source is observed" (RFC 3501 §6.4.6). This
// engine observes another source's changes when an idling session re-reads its
// mailbox, and reports each with an untagged FETCH carrying the message's flags,
// after a silent STORE as after any other.
//
// checklist: IMAP-STORE-12
// checklist: IMAP2-CMD-155
func TestStoreSilentStillReportsOtherSourcesChanges(t *testing.T) {
	box := &sharedMailbox{messages: []imap.Message{{UID: 5001}, {UID: 5002}, {UID: 5003}}}
	c := dialConfigured(t, box, nil, nil, func(s *server.Session) { s.SetIdlePollInterval(20 * time.Millisecond) })
	c.line()
	c.login()
	if _, tagged := c.command("S1", "SELECT INBOX"); !strings.HasPrefix(tagged, "S1 OK") {
		t.Fatalf("SELECT: %q", tagged)
	}

	untagged, tagged := c.command("A1", `STORE 2 +FLAGS.SILENT (\Flagged)`)
	requireOK(t, tagged, "A1")
	requireExactly(t, untagged)

	// Another client marks message 3 \Seen.
	box.set(5003, func(msg *imap.Message) { msg.Seen = true })

	if _, err := fmt.Fprintf(c.conn, "A2 IDLE\r\n"); err != nil {
		t.Fatal(err)
	}
	if got := c.line(); !strings.HasPrefix(got, "+") {
		t.Fatalf("IDLE continuation: %q", got)
	}
	if got := c.line(); got != `* 3 FETCH (FLAGS (\Seen))` {
		t.Fatalf("the other source's change: %q, want * 3 FETCH (FLAGS (\\Seen))", got)
	}
	if _, err := fmt.Fprintf(c.conn, "DONE\r\n"); err != nil {
		t.Fatal(err)
	}
	if got := c.line(); !strings.HasPrefix(got, "A2 OK") {
		t.Fatalf("IDLE completion: %q", got)
	}

	// The session's own silent change was not reported as anyone else's.
	untagged, tagged = c.command("A3", `FETCH 2 (FLAGS)`)
	requireOK(t, tagged, "A3")
	if len(untagged) != 1 || !strings.Contains(untagged[0], `FLAGS (\Flagged)`) {
		t.Fatalf("message 2: %q", untagged)
	}
}
