package rfctest

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// storeMailbox holds one INBOX whose backend flag writes are recorded, and can
// be told to refuse them.
type storeMailbox struct {
	mailbox
	messages []imap.Message
	updates  map[uint32][]imap.FlagUpdate
	fail     bool
}

func newStoreMailbox() *storeMailbox {
	// RFC 3501 §6.4.6's example: message 2 is \Seen, 3 unflagged and 4
	// \Flagged \Seen before A003 adds \Deleted to 2:4.
	return &storeMailbox{
		messages: []imap.Message{
			{UID: 4001},
			{UID: 4002, Seen: true},
			{UID: 4003},
			{UID: 4004, Seen: true, Flagged: true},
		},
		updates: map[uint32][]imap.FlagUpdate{},
	}
}

func (m *storeMailbox) Messages(folder string) ([]imap.Message, error) {
	if folder != "INBOX" {
		return nil, errors.New("mailbox unavailable")
	}
	return slices.Clone(m.messages), nil
}

func (m *storeMailbox) Store(uid uint32, update imap.FlagUpdate) error {
	if m.fail {
		return errors.New("store refused")
	}
	m.updates[uid] = append(m.updates[uid], update)
	for index := range m.messages {
		message := &m.messages[index]
		if message.UID != uid {
			continue
		}
		if update.Deleted != nil {
			message.Deleted = *update.Deleted
		}
		if update.Seen != nil {
			message.Seen = *update.Seen
		}
		if update.Answered != nil {
			message.Answered = *update.Answered
		}
		if update.Flagged != nil {
			message.Flagged = *update.Flagged
		}
		if update.Draft != nil {
			message.Draft = *update.Draft
		}
		if update.KeywordsSet {
			message.Keywords = slices.Clone(update.Keywords)
		}
	}
	return nil
}

func storeClient(t *testing.T, box *storeMailbox) *client {
	t.Helper()
	c := selectionClient(t, box)
	if _, tagged := c.command("S1", "SELECT INBOX"); !strings.HasPrefix(tagged, "S1 OK") {
		t.Fatalf("SELECT: %q", tagged)
	}
	return c
}

func requireOK(t *testing.T, tagged, tag string) {
	t.Helper()
	if !strings.HasPrefix(tagged, tag+" OK") {
		t.Fatalf("%s: %q, want tagged OK", tag, tagged)
	}
}

func requireExactly(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("untagged responses %q, want %q", got, want)
	}
}

// TestStoreExample replays RFC 3501 §6.4.6's +FLAGS example, then removes and
// replaces flags. Each change answers with an untagged FETCH carrying the
// message's full resulting flag list (in this engine's flag order). This
// engine's plain FETCH also volunteers UID, which RFC 3501 §7.4.2 permits.
//
// checklist: IMAP-STORE-01 IMAP-STORE-02
// checklist: IMAP2-SEC-216
func TestStoreExample(t *testing.T) {
	box := newStoreMailbox()
	c := storeClient(t, box)

	untagged, tagged := c.command("A003", `STORE 2:4 +FLAGS (\Deleted)`)
	requireOK(t, tagged, "A003")
	requireExactly(t, untagged,
		`* 2 FETCH (FLAGS (\Seen \Deleted))`,
		`* 3 FETCH (FLAGS (\Deleted))`,
		`* 4 FETCH (FLAGS (\Seen \Flagged \Deleted))`,
	)

	untagged, tagged = c.command("A004", `STORE 2 -FLAGS (\Seen)`)
	requireOK(t, tagged, "A004")
	requireExactly(t, untagged, `* 2 FETCH (FLAGS (\Deleted))`)

	untagged, tagged = c.command("A005", `STORE 4 FLAGS (\Answered)`)
	requireOK(t, tagged, "A005")
	requireExactly(t, untagged, `* 4 FETCH (FLAGS (\Answered))`)

	untagged, tagged = c.command("A006", "FETCH 1:4 (FLAGS)")
	requireOK(t, tagged, "A006")
	requireExactly(t, untagged,
		`* 1 FETCH (UID 4001 FLAGS ())`,
		`* 2 FETCH (UID 4002 FLAGS (\Deleted))`,
		`* 3 FETCH (UID 4003 FLAGS (\Deleted))`,
		`* 4 FETCH (UID 4004 FLAGS (\Answered))`,
	)

	// The persistent flags reached the backend; \Deleted stays session state.
	if got := box.updates[4002]; len(got) != 2 || got[0].Deleted == nil || !*got[0].Deleted || got[1].Seen == nil || *got[1].Seen {
		t.Fatalf("message 2 backend updates %+v, want Deleted=true then Seen=false", got)
	}
	if got := box.updates[4004]; len(got) != 2 || got[0].Deleted == nil || got[1].Answered == nil || !*got[1].Answered ||
		got[1].Seen == nil || *got[1].Seen || got[1].Flagged == nil || *got[1].Flagged {
		t.Fatalf("message 4 backend updates %+v, want replace with Answered only", got)
	}
}

// TestStoreSilent proves .SILENT suppresses the untagged FETCH while still
// applying and persisting the change.
//
// checklist: IMAP-STORE-03
// checklist: IMAP2-CMD-154
func TestStoreSilent(t *testing.T) {
	box := newStoreMailbox()
	c := storeClient(t, box)

	untagged, tagged := c.command("A1", `STORE 1 +FLAGS.SILENT (\Flagged)`)
	requireOK(t, tagged, "A1")
	requireExactly(t, untagged)

	untagged, tagged = c.command("A2", `UID STORE 4003 FLAGS.SILENT (\Seen)`)
	requireOK(t, tagged, "A2")
	requireExactly(t, untagged)

	untagged, tagged = c.command("A3", "FETCH 1,3 (FLAGS)")
	requireOK(t, tagged, "A3")
	requireExactly(t, untagged, `* 1 FETCH (UID 4001 FLAGS (\Flagged))`, `* 3 FETCH (UID 4003 FLAGS (\Seen))`)
	if len(box.updates[4001]) != 1 || len(box.updates[4003]) != 1 {
		t.Fatalf("backend updates %+v, want one each for 4001 and 4003", box.updates)
	}
}

// TestUIDStoreReportsUID proves a FETCH caused by UID STORE carries the UID.
//
// checklist: IMAP-STORE-04
// checklist: IMAP2-CMD-166
func TestUIDStoreReportsUID(t *testing.T) {
	c := storeClient(t, newStoreMailbox())

	untagged, tagged := c.command("A1", `UID STORE 4002:4003 +FLAGS (\Flagged)`)
	requireOK(t, tagged, "A1")
	requireExactly(t, untagged,
		`* 2 FETCH (UID 4002 FLAGS (\Seen \Flagged))`,
		`* 3 FETCH (UID 4003 FLAGS (\Flagged))`,
	)
}

// TestStoreBackendFailure proves a refused backend write answers NO and leaves
// the session's view of the message unchanged, for STORE and UID STORE.
//
// checklist: IMAP-STORE-05
func TestStoreBackendFailure(t *testing.T) {
	for _, command := range []string{`STORE 2 FLAGS (\Flagged \Deleted)`, `UID STORE 4002 FLAGS (\Flagged \Deleted)`} {
		t.Run(strings.Fields(command)[0], func(t *testing.T) {
			box := newStoreMailbox()
			box.fail = true
			c := storeClient(t, box)

			untagged, tagged := c.command("A1", "%s", command)
			if !strings.HasPrefix(tagged, "A1 NO") {
				t.Fatalf("refused store: %q, want tagged NO", tagged)
			}
			requireExactly(t, untagged)

			untagged, tagged = c.command("A2", "FETCH 2 (FLAGS)")
			requireOK(t, tagged, "A2")
			requireExactly(t, untagged, `* 2 FETCH (UID 4002 FLAGS (\Seen))`)
		})
	}
}
