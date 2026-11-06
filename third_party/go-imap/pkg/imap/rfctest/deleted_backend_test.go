package rfctest

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// deletedStore is one INBOX shared by every session, like the store behind a
// real server: it keeps \Deleted itself and says so through
// imap.DeletedMailbox, so a mark one session sets is the mark every session
// sees (RFC 3501 §2.3.2).
type deletedStore struct {
	mailbox
	mu       sync.Mutex
	messages []imap.Message
	deleted  []uint32 // UIDs the store removed, in order
	failRead bool
}

func newDeletedStore() *deletedStore {
	return &deletedStore{messages: []imap.Message{{UID: 10}, {UID: 20}, {UID: 30}, {UID: 40}}}
}

func (m *deletedStore) Messages(folder string) ([]imap.Message, error) {
	if folder != "INBOX" {
		return nil, errors.New("mailbox unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.messages), nil
}

func (m *deletedStore) Store(uid uint32, update imap.FlagUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.messages {
		if m.messages[i].UID != uid {
			continue
		}
		if update.Deleted != nil {
			m.messages[i].Deleted = *update.Deleted
		}
		if update.Seen != nil {
			m.messages[i].Seen = *update.Seen
		}
	}
	return nil
}

func (m *deletedStore) Delete(uid uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, msg := range m.messages {
		if msg.UID == uid {
			m.messages = slices.Delete(m.messages, i, i+1)
			m.deleted = append(m.deleted, uid)
			return nil
		}
	}
	return errors.New("unknown UID")
}

func (m *deletedStore) DeletedUIDs(folder string) ([]uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failRead || folder != "INBOX" {
		return nil, errors.New("store unavailable")
	}
	var uids []uint32
	for _, msg := range m.messages {
		if msg.Deleted {
			uids = append(uids, msg.UID)
		}
	}
	return uids, nil
}

func (*deletedStore) UIDValidity(string) (uint32, error) { return 7, nil }
func (*deletedStore) AppendUID(string, imap.FlagUpdate, []byte) (uint32, error) {
	return 0, errors.New("unused")
}
func (*deletedStore) CopyUID(uint32, string) (uint32, error) { return 0, errors.New("unused") }

func (m *deletedStore) removed() []uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.deleted)
}

func deletedClient(t *testing.T, store *deletedStore) *client {
	t.Helper()
	c := selectionClient(t, store)
	_, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	return c
}

func requireDeletedFlag(t *testing.T, c *client, seq string, want bool) {
	t.Helper()
	lines, tagged := c.command("F", "FETCH %s FLAGS", seq)
	requireOK(t, tagged, "F")
	if len(lines) != 1 || strings.Contains(lines[0], `\Deleted`) != want {
		t.Fatalf("FETCH %s FLAGS = %q, want \\Deleted %v", seq, lines, want)
	}
}

// TestDeletedMarkReachesEverySession: a mark set in one session is in the
// store, so a session already open sees it on NOOP and one opened after the
// first has gone (a dropped connection, the user's phone) sees it on SELECT
// (RFC 3501 §2.3.2: \Deleted is a permanent flag).
//
// checklist: IMAP-EXPUNGE-07
func TestDeletedMarkReachesEverySession(t *testing.T) {
	store := newDeletedStore()
	desktop := deletedClient(t, store)
	phone := deletedClient(t, store)

	_, tagged := desktop.command("D1", `STORE 2 +FLAGS (\Deleted)`)
	requireOK(t, tagged, "D1")

	// Unsolicited FETCH carries the UID (RFC 9051 §7.5.2).
	lines, tagged := phone.command("N1", "NOOP")
	requireOK(t, tagged, "N1")
	requireLine(t, lines, `* 2 FETCH (UID 20 FLAGS (\Deleted))`)
	requireDeletedFlag(t, phone, "2", true)

	_ = desktop.conn.Close()
	later := deletedClient(t, store)
	requireDeletedFlag(t, later, "2", true)
	requireDeletedFlag(t, later, "1", false)
	if got := store.removed(); len(got) != 0 {
		t.Fatalf("store removed %v before any EXPUNGE", got)
	}
}

// TestExpungeRemovesTheStoredMarks: EXPUNGE removes the messages the store
// marks now (RFC 3501 §6.4.3), not the marks the session last saw: one the
// other session marked goes, one it unmarked stays, and unmarked messages are
// never touched.
//
// checklist: IMAP-EXPUNGE-07
func TestExpungeRemovesTheStoredMarks(t *testing.T) {
	store := newDeletedStore()
	desktop := deletedClient(t, store)
	phone := deletedClient(t, store)

	_, tagged := desktop.command("D1", `STORE 3 +FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "D1")
	_, tagged = phone.command("D2", `STORE 2 +FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "D2")
	_, tagged = phone.command("D3", `STORE 3 -FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "D3")

	lines, tagged := desktop.command("E1", "EXPUNGE")
	requireOK(t, tagged, "E1")
	requireExactly(t, lines, "* 2 EXPUNGE")
	if got := store.removed(); !slices.Equal(got, []uint32{20}) {
		t.Fatalf("store removed %v, want [20]", got)
	}
}

// TestUIDExpungeRemovesOnlyStoredMarksInItsSet: UID EXPUNGE removes the stored
// marks inside its UID set and leaves the rest marked (RFC 4315 §2.1).
//
// checklist: IMAP-EXPUNGE-07
func TestUIDExpungeRemovesOnlyStoredMarksInItsSet(t *testing.T) {
	store := newDeletedStore()
	desktop := deletedClient(t, store)
	phone := deletedClient(t, store)

	_, tagged := phone.command("D1", `STORE 1,3 +FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "D1")

	lines, tagged := desktop.command("U1", "UID EXPUNGE 20:30")
	requireOK(t, tagged, "U1")
	requireExactly(t, lines, "* 3 EXPUNGE")
	if got := store.removed(); !slices.Equal(got, []uint32{30}) {
		t.Fatalf("store removed %v, want [30]", got)
	}
	ids, _ := store.DeletedUIDs("INBOX")
	if !slices.Equal(ids, []uint32{10}) {
		t.Fatalf("stored marks %v after UID EXPUNGE, want [10]", ids)
	}
}

// TestCloseRemovesTheStoredMarks: CLOSE expunges silently from the same stored
// marks (RFC 3501 §6.4.2).
//
// checklist: IMAP-EXPUNGE-07
func TestCloseRemovesTheStoredMarks(t *testing.T) {
	store := newDeletedStore()
	desktop := deletedClient(t, store)
	phone := deletedClient(t, store)

	_, tagged := desktop.command("D1", `STORE 1 +FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "D1")
	_, tagged = phone.command("D2", `STORE 1 -FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "D2")
	_, tagged = phone.command("D3", `STORE 4 +FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "D3")

	lines, tagged := desktop.command("C1", "CLOSE")
	requireOK(t, tagged, "C1")
	requireExactly(t, lines)
	if got := store.removed(); !slices.Equal(got, []uint32{40}) {
		t.Fatalf("store removed %v, want [40]", got)
	}
}

// TestExpungeRefusedWhenStoredMarksUnreadable: when the store cannot say which
// messages are marked, EXPUNGE and CLOSE answer NO and remove nothing, rather
// than guess from the session's view.
//
// checklist: IMAP-EXPUNGE-07
func TestExpungeRefusedWhenStoredMarksUnreadable(t *testing.T) {
	store := newDeletedStore()
	c := deletedClient(t, store)
	_, tagged := c.command("D1", `STORE 1 +FLAGS.SILENT (\Deleted)`)
	requireOK(t, tagged, "D1")
	store.mu.Lock()
	store.failRead = true
	store.mu.Unlock()

	for _, command := range []string{"EXPUNGE", "UID EXPUNGE 10", "CLOSE"} {
		lines, tagged := c.command("E", "%s", command)
		if !strings.HasPrefix(tagged, "E NO") {
			t.Fatalf("%s: %q, want NO", command, tagged)
		}
		requireExactly(t, lines)
	}
	if got := store.removed(); len(got) != 0 {
		t.Fatalf("store removed %v with unreadable marks", got)
	}
	requireDeletedFlag(t, c, "1", true)
}
