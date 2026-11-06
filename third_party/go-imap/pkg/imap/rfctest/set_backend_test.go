package rfctest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// setStore copies and moves a message set as one transaction
// (imap.SetMailbox): it works on a copy of its folders and keeps the result
// only when every message succeeded, as a database transaction would. failAt
// makes the nth message of a set (1-based) fail, after the ones before it
// were already done in the transaction.
type setStore struct {
	mailbox
	mu      sync.Mutex
	folders map[string][]imap.Message
	nextUID uint32
	failAt  int
	calls   int // set calls made
	single  int // per-message calls made
}

func newSetStore() *setStore {
	return &setStore{
		folders: map[string][]imap.Message{
			"INBOX":   {{UID: 10}, {UID: 20}, {UID: 30}, {UID: 40}},
			"Archive": {{UID: 5}},
		},
		nextUID: 100,
	}
}

func (m *setStore) Folders() ([]imap.Folder, error) {
	return []imap.Folder{{Name: "INBOX"}, {Name: "Archive"}}, nil
}

func (m *setStore) Messages(folder string) ([]imap.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs, ok := m.folders[folder]
	if !ok {
		return nil, imap.ErrNoSuchMailbox
	}
	return slices.Clone(msgs), nil
}

func (m *setStore) uids(folder string) []uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []uint32
	for _, msg := range m.folders[folder] {
		out = append(out, msg.UID)
	}
	return out
}

func (m *setStore) run(folder string, uids []uint32, dest string, move bool) ([]uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if _, ok := m.folders[dest]; !ok {
		return nil, imap.ErrNoSuchMailbox
	}
	// The transaction: a working copy, kept only when every row succeeds.
	work := map[string][]imap.Message{}
	for name, msgs := range m.folders {
		work[name] = slices.Clone(msgs)
	}
	next := m.nextUID
	var out []uint32
	for i, uid := range uids {
		if m.failAt == i+1 {
			return nil, fmt.Errorf("row %d failed", i+1)
		}
		at := slices.IndexFunc(work[folder], func(msg imap.Message) bool { return msg.UID == uid })
		if at < 0 {
			return nil, errors.New("unknown UID")
		}
		work[dest] = append(work[dest], imap.Message{UID: next})
		if move {
			work[folder] = slices.Delete(work[folder], at, at+1)
		}
		out = append(out, next)
		next++
	}
	m.folders, m.nextUID = work, next
	return out, nil
}

func (m *setStore) CopyMessages(folder string, uids []uint32, dest string) ([]uint32, error) {
	return m.run(folder, uids, dest, false)
}

func (m *setStore) MoveMessages(folder string, uids []uint32, dest string) ([]uint32, error) {
	return m.run(folder, uids, dest, true)
}

// The per-message forms must not be used while the set forms exist.
func (m *setStore) Copy(uint32, string) error { m.single++; return errors.New("per-message copy") }
func (m *setStore) Move(uint32, string) error { m.single++; return errors.New("per-message move") }
func (m *setStore) CopyUID(uint32, string) (uint32, error) {
	m.single++
	return 0, errors.New("per-message copy")
}
func (m *setStore) MoveUID(uint32, string) (uint32, error) {
	m.single++
	return 0, errors.New("per-message move")
}
func (*setStore) UIDValidity(string) (uint32, error) { return 9, nil }
func (*setStore) AppendUID(string, imap.FlagUpdate, []byte) (uint32, error) {
	return 0, errors.New("unused")
}

func setClient(t *testing.T, store *setStore) *client {
	t.Helper()
	c := selectionClient(t, store)
	_, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	return c
}

func requireSetUIDs(t *testing.T, store *setStore, folder string, want ...uint32) {
	t.Helper()
	if got := store.uids(folder); !slices.Equal(got, want) {
		t.Fatalf("%s holds UIDs %v, want %v", folder, got, want)
	}
}

// TestFailedSetCopyLeavesTheDestinationUnchanged: the store fails on the 3rd
// message of the set, so COPY and UID COPY answer NO, the destination holds
// what it held before and the source is untouched (RFC 3501 §6.4.7, RFC 9051
// §6.4.7: an unsuccessful COPY MUST restore the destination).
//
// checklist: IMAP-COPY-11
func TestFailedSetCopyLeavesTheDestinationUnchanged(t *testing.T) {
	for _, command := range []string{"COPY 1:4 Archive", "UID COPY 10:40 Archive"} {
		t.Run(command, func(t *testing.T) {
			store := newSetStore()
			store.failAt = 3
			c := setClient(t, store)
			untagged, tagged := c.command("C1", "%s", command)
			if !strings.HasPrefix(tagged, "C1 NO") || strings.Contains(tagged, "COPYUID") {
				t.Fatalf("%s = %q, want NO without COPYUID", command, tagged)
			}
			if len(untagged) != 0 {
				t.Fatalf("%s sent %q, want nothing untagged", command, untagged)
			}
			requireSetUIDs(t, store, "Archive", 5)
			requireSetUIDs(t, store, "INBOX", 10, 20, 30, 40)
			if store.calls != 1 || store.single != 0 {
				t.Fatalf("store saw %d set and %d per-message calls, want one set call", store.calls, store.single)
			}
		})
	}
}

// TestSetCopyReportsEveryCopy: a set copy that succeeds is one store call and
// COPYUID maps each source UID to the UID its copy got, in order (RFC 4315
// §3).
//
// checklist: IMAP-COPY-11
func TestSetCopyReportsEveryCopy(t *testing.T) {
	store := newSetStore()
	c := setClient(t, store)
	_, tagged := c.command("C1", "COPY 2,4,1 Archive")
	if tagged != "C1 OK [COPYUID 9 20,40,10 100,101,102] COPY completed" {
		t.Fatalf("COPY = %q", tagged)
	}
	requireSetUIDs(t, store, "Archive", 5, 100, 101, 102)
	requireSetUIDs(t, store, "INBOX", 10, 20, 30, 40)
	if store.calls != 1 || store.single != 0 {
		t.Fatalf("store saw %d set and %d per-message calls, want one set call", store.calls, store.single)
	}
}

// TestFailedSetMoveLeavesBothMailboxes: the store fails on the 3rd message,
// so MOVE and UID MOVE answer NO, expunge nothing and both mailboxes hold what
// they held: no message is in both or lost (RFC 6851 §3.3).
//
// checklist: IMAP-MOVE-10
func TestFailedSetMoveLeavesBothMailboxes(t *testing.T) {
	for _, command := range []string{"MOVE 1:4 Archive", "UID MOVE 10:40 Archive"} {
		t.Run(command, func(t *testing.T) {
			store := newSetStore()
			store.failAt = 3
			c := setClient(t, store)
			untagged, tagged := c.command("M1", "%s", command)
			if !strings.HasPrefix(tagged, "M1 NO") {
				t.Fatalf("%s = %q, want NO", command, tagged)
			}
			if len(untagged) != 0 {
				t.Fatalf("%s sent %q, want no COPYUID or EXPUNGE", command, untagged)
			}
			requireSetUIDs(t, store, "Archive", 5)
			requireSetUIDs(t, store, "INBOX", 10, 20, 30, 40)
			_, tagged = c.command("N1", "NOOP")
			requireOK(t, tagged, "N1")
			lines, tagged := c.command("F1", "FETCH 1:* UID")
			requireOK(t, tagged, "F1")
			if len(lines) != 4 {
				t.Fatalf("after the failed move the session sees %q, want all four", lines)
			}
		})
	}
}

// TestSetMoveReportsAndExpungesEveryMessage: a set move that succeeds is one
// store call; COPYUID comes first, then an EXPUNGE per moved message (RFC
// 6851 §§3.3, 4).
//
// checklist: IMAP-MOVE-10
func TestSetMoveReportsAndExpungesEveryMessage(t *testing.T) {
	store := newSetStore()
	c := setClient(t, store)
	untagged, tagged := c.command("M1", "UID MOVE 20,40 Archive")
	requireOK(t, tagged, "M1")
	want := []string{"* OK [COPYUID 9 20,40 100,101] Moved", "* 4 EXPUNGE", "* 2 EXPUNGE"}
	if !slices.Equal(untagged, want) {
		t.Fatalf("UID MOVE sent %q, want %q", untagged, want)
	}
	requireSetUIDs(t, store, "Archive", 5, 100, 101)
	requireSetUIDs(t, store, "INBOX", 10, 30)
	if store.calls != 1 || store.single != 0 {
		t.Fatalf("store saw %d set and %d per-message calls, want one set call", store.calls, store.single)
	}
}
