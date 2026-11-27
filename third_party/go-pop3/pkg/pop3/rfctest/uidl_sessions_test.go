package rfctest

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// Sequential sessions share this synchronized memory model. Messages returns a
// fresh snapshot reflecting committed Delete calls; it provides no disk storage,
// UID generation, crash recovery or maildrop lifetime/non-reuse guarantee.
type uidSessionMailbox struct {
	mu       sync.Mutex
	messages []pop3.Message
	deletes  []string
}

func (m *uidSessionMailbox) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if user != "alice" || pass != "secret" {
		return nil, errors.New("invalid fixture credentials")
	}
	return m, nil
}
func (m *uidSessionMailbox) Messages() ([]pop3.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]pop3.Message(nil), m.messages...), nil
}
func (*uidSessionMailbox) Retrieve(string) ([]byte, error) {
	return nil, errors.New("unused fixture retrieval")
}
func (*uidSessionMailbox) MarkSeen(string) error { return errors.New("unused fixture mark-seen") }
func (m *uidSessionMailbox) Delete(uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, uid)
	for i, msg := range m.messages {
		if msg.UID == uid {
			m.messages = append(m.messages[:i], m.messages[i+1:]...)
			return nil
		}
	}
	return errors.New("unknown or already removed fixture UID")
}
func (m *uidSessionMailbox) add(msg pop3.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
}
func (m *uidSessionMailbox) checkDeletes(t *testing.T, want []string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !reflect.DeepEqual(m.deletes, want) {
		t.Fatalf("backend deletions = %v, want %v", m.deletes, want)
	}
}

// Expected order and identities are supplied independently of backend snapshots
// and wire responses. Exact list length, terminator and a following reply detect
// removed/extra UIDs, trailing fields and framing errors.
func sessionUIDs(t *testing.T, w *wire, want ...string) {
	t.Helper()
	expect(t, w.cmd("UIDL"), "+OK")
	for i, uid := range want {
		expect(t, w.line(), fmt.Sprintf("%d %s", i+1, uid))
	}
	expect(t, w.line(), ".")
	expect(t, w.cmd("NOOP"), "+OK")
	for i, uid := range want {
		expect(t, w.cmd(fmt.Sprintf("UIDL %d", i+1)), fmt.Sprintf("+OK %d %s", i+1, uid))
	}
	w.status(w.cmd(fmt.Sprintf("UIDL %d", len(want)+1)), "-ERR")
}

// checklist: POP3-UIDL-05
func TestUIDLSequentialSessionsDeleteAndAppend(t *testing.T) {
	m := &uidSessionMailbox{messages: []pop3.Message{
		{UID: "opaque:removed-A", Size: 11},
		{UID: "opaque:retained-B", Size: 23},
	}}
	first := connect(t, m)
	first.login("secret")
	expect(t, first.cmd("STAT"), "+OK 2 34")
	sessionUIDs(t, first, "opaque:removed-A", "opaque:retained-B")
	expect(t, first.cmd("DELE 1"), "+OK Message 1 deleted")
	m.checkDeletes(t, nil)
	quit(t, first, "+OK") // EOF and join before starting another public Session.
	m.checkDeletes(t, []string{"opaque:removed-A"})

	second := connect(t, m)
	second.login("secret")
	expect(t, second.cmd("STAT"), "+OK 1 23")
	// The original wire message 2 is now 1; its opaque UID must not change.
	sessionUIDs(t, second, "opaque:retained-B")
	quit(t, second, "+OK")
	m.checkDeletes(t, []string{"opaque:removed-A"})

	m.add(pop3.Message{UID: "opaque:new-C", Size: 37})
	third := connect(t, m)
	third.login("secret")
	expect(t, third.cmd("STAT"), "+OK 2 60")
	sessionUIDs(t, third, "opaque:retained-B", "opaque:new-C")
	quit(t, third, "+OK")
	m.checkDeletes(t, []string{"opaque:removed-A"})
}

// checklist: POP3-UIDL-06
func TestUIDLSequentialSessionsWithoutUpdate(t *testing.T) {
	m := &uidSessionMailbox{messages: []pop3.Message{
		{UID: "opaque:kept-X", Size: 11},
		{UID: "opaque:kept-Y", Size: 23},
	}}
	first := connect(t, m)
	first.login("secret")
	sessionUIDs(t, first, "opaque:kept-X", "opaque:kept-Y")
	expect(t, first.cmd("DELE 1"), "+OK Message 1 deleted")
	if err := first.conn.Close(); err != nil {
		t.Fatal(err)
	}
	waitStopped(t, first) // EOF without QUIT must not commit the deletion mark.
	m.checkDeletes(t, nil)

	second := connect(t, m)
	second.login("secret")
	expect(t, second.cmd("STAT"), "+OK 2 34")
	sessionUIDs(t, second, "opaque:kept-X", "opaque:kept-Y")
	quit(t, second, "+OK")
	m.checkDeletes(t, nil)
}
