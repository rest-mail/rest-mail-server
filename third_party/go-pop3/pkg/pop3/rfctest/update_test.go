package rfctest

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// Opaque UIDs differ from wire message numbers. This fixture models successful
// per-message persistence, not atomic updates or real backend ownership/locking.
var updateUIDs = []string{"uid-alpha", "uid-bravo", "uid-charlie"}

type updateMailbox struct {
	transactionMailbox
	fail     map[string]bool
	attempts map[string]int
	removed  map[string]bool
}

func (m *updateMailbox) Authenticate(string, string) (pop3.Mailbox, error) { return m, nil }
func (m *updateMailbox) Messages() ([]pop3.Message, error) {
	msgs, err := m.transactionMailbox.Messages()
	for i := range msgs {
		msgs[i].UID = updateUIDs[i]
	}
	return msgs, err
}
func (m *updateMailbox) Delete(uid string) error {
	m.attempts[uid]++
	if m.fail[uid] {
		return errors.New("fixture delete failure")
	}
	m.removed[uid] = true
	return nil
}
func update(t *testing.T, fail map[string]bool) (*wire, *updateMailbox) {
	t.Helper()
	m := &updateMailbox{
		transactionMailbox: transactionMailbox{raw: transactionMessages},
		fail:               fail, attempts: map[string]int{}, removed: map[string]bool{},
	}
	w := connect(t, m)
	w.login("secret")
	return w, m
}
func waitStopped(t *testing.T, w *wire) {
	t.Helper()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop")
	}
}
func quit(t *testing.T, w *wire, status string) {
	t.Helper()
	w.status(w.cmd("QUIT"), status)
	_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := w.reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("after QUIT: %v, want EOF", err)
	}
	waitStopped(t, w)
}
func deletions(t *testing.T, m *updateMailbox, attempted, removed []int) {
	t.Helper()
	wantAttempts, wantRemoved := map[string]int{}, map[string]bool{}
	for _, n := range attempted {
		wantAttempts[updateUIDs[n-1]]++
	}
	for _, n := range removed {
		wantRemoved[updateUIDs[n-1]] = true
	}
	if !reflect.DeepEqual(m.attempts, wantAttempts) || !reflect.DeepEqual(m.removed, wantRemoved) {
		t.Fatalf("backend attempts/removals = %v/%v, want %v/%v", m.attempts, m.removed, wantAttempts, wantRemoved)
	}
}

// checklist: POP3-UPDATE-01 POP3-UPDATE-02 POP3-UPDATE-03 POP3-UPDATE-04
func TestUpdateQuit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		marks []int
	}{
		{"none", nil}, {"sparse", []int{1, 3}}, {"all", []int{1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, m := update(t, nil)
			for _, n := range tc.marks {
				w.status(w.cmd(fmt.Sprintf("DELE %d", n)), "+OK")
			}
			// Invalid QUIT must neither commit nor end TRANSACTION.
			w.status(w.cmd("QUIT unexpected"), "-ERR")
			w.status(w.cmd("NOOP"), "+OK")
			deletions(t, m, nil, nil)
			quit(t, w, "+OK")
			deletions(t, m, tc.marks, tc.marks)
		})
	}
}

// checklist: POP3-UPDATE-02 POP3-UPDATE-03
func TestUpdateResetQuit(t *testing.T) {
	w, m := update(t, nil)
	for _, n := range []int{1, 2, 3} {
		w.status(w.cmd(fmt.Sprintf("DELE %d", n)), "+OK")
	}
	w.status(w.cmd("RSET"), "+OK")
	quit(t, w, "+OK")
	deletions(t, m, nil, nil)
}

// checklist: POP3-UPDATE-05
func TestUpdateDisconnect(t *testing.T) {
	for _, partial := range []string{"", "QUI"} {
		t.Run(fmt.Sprintf("partial=%q", partial), func(t *testing.T) {
			w, m := update(t, nil)
			for _, n := range []int{1, 3} {
				w.status(w.cmd(fmt.Sprintf("DELE %d", n)), "+OK")
			}
			if partial != "" {
				_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err := io.WriteString(w.conn, partial); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.conn.Close(); err != nil {
				t.Fatal(err)
			}
			waitStopped(t, w)
			deletions(t, m, nil, nil)
		})
	}
}

// checklist: POP3-UPDATE-02 POP3-UPDATE-03 POP3-UPDATE-06
func TestUpdateDeleteFailure(t *testing.T) {
	for _, tc := range []struct {
		name           string
		marks, removed []int
	}{
		{"none-removed", []int{1}, nil},
		{"some-removed", []int{1, 3}, []int{3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, m := update(t, map[string]bool{updateUIDs[0]: true})
			for _, n := range tc.marks {
				w.status(w.cmd(fmt.Sprintf("DELE %d", n)), "+OK")
			}
			quit(t, w, "-ERR")
			// RFC 1939 permits partial removal on failure. The current server
			// attempts each marked UID; neither order nor rollback is promised.
			deletions(t, m, tc.marks, tc.removed)
		})
	}
}
