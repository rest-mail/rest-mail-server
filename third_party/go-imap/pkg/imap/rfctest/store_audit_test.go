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
// (§2.3.2). Naming it in a FLAGS, +FLAGS or -FLAGS list changes no flag, and it
// never appears in the flags a STORE answers with.
//
// checklist: IMAP-STORE-11
func TestStoreLeavesRecentAlone(t *testing.T) {
	box := newStoreMailbox()
	c := storeClient(t, box)

	untagged, tagged := c.command("A1", `STORE 1 FLAGS (\Recent \Seen)`)
	requireOK(t, tagged, "A1")
	requireExactly(t, untagged, `* 1 FETCH (FLAGS (\Seen))`)

	untagged, tagged = c.command("A2", `STORE 1 -FLAGS (\Recent)`)
	requireOK(t, tagged, "A2")
	requireExactly(t, untagged, `* 1 FETCH (FLAGS (\Seen))`)

	untagged, tagged = c.command("A3", `STORE 1 +FLAGS (\Recent)`)
	requireOK(t, tagged, "A3")
	requireExactly(t, untagged, `* 1 FETCH (FLAGS (\Seen))`)

	untagged, tagged = c.command("A4", `STORE 1 FLAGS ()`)
	requireOK(t, tagged, "A4")
	requireExactly(t, untagged, `* 1 FETCH (FLAGS ())`)
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
