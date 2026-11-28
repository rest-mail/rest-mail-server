package rfctest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// Reuse the transaction corpus, but keep backend handles distinct from wire
// numbers. No backend persistence across sessions is modeled here.
type optionalMailbox struct {
	transactionMailbox
	uids []string
}

func (m *optionalMailbox) Authenticate(string, string) (pop3.Mailbox, error) { return m, nil }
func (m *optionalMailbox) Messages() ([]pop3.Message, error) {
	msgs, err := m.transactionMailbox.Messages()
	for i := range msgs {
		msgs[i].UID = m.uids[i]
	}
	return msgs, err
}
func (m *optionalMailbox) Retrieve(uid string) ([]byte, error) {
	for i, id := range m.uids {
		if id == uid {
			return []byte(m.raw[i]), nil
		}
	}
	return nil, errors.New("unknown opaque UID")
}
func optional(t *testing.T, raw, uids []string) *wire {
	t.Helper()
	m := &optionalMailbox{transactionMailbox: transactionMailbox{raw: raw}, uids: uids}
	w := connect(t, m)
	w.login("secret")
	return w
}
func top(t *testing.T, w *wire, command string, lines ...string) {
	t.Helper()
	w.status(w.cmd(command), "+OK")
	for _, line := range lines {
		expect(t, w.line(), line)
	}
	expect(t, w.line(), ".")
	// Detect extra body lines or terminators left behind by this response.
	w.status(w.cmd("NOOP"), "+OK")
}
func uidListing(t *testing.T, w *wire, uids []string, numbers ...int) {
	t.Helper()
	w.status(w.cmd("UIDL"), "+OK")
	for _, n := range numbers {
		expect(t, w.line(), fmt.Sprintf("%d %s", n, uids[n-1]))
	}
	expect(t, w.line(), ".")
	w.status(w.cmd("NOOP"), "+OK")
	for _, n := range numbers {
		expect(t, w.cmd(fmt.Sprintf("UIDL %d", n)), fmt.Sprintf("+OK %d %s", n, uids[n-1]))
	}
}

// checklist: POP3-TOP-01 POP3-UIDL-01
func TestOptionalArguments(t *testing.T) {
	m := &optionalMailbox{transactionMailbox: transactionMailbox{raw: transactionMessages}, uids: updateUIDs}
	w := connect(t, m)
	for _, command := range []string{"TOP 1 0", "UIDL", "UIDL 1"} {
		w.status(w.cmd(command), "-ERR")
	}
	w.login("secret")
	for _, command := range []string{
		"TOP", "TOP 1", "TOP 1 -1", "TOP 1 nonsense", "TOP 1 0 1",
		"TOP 0 0", "TOP -1 0", "TOP 4 0", "TOP nonsense 0",
		"UIDL 0", "UIDL -1", "UIDL 4", "UIDL nonsense", "UIDL 1 2",
	} {
		w.status(w.cmd(command), "-ERR")
	}
	top(t, w, "TOP 1 0", "Subject: a", "")
	uidListing(t, w, updateUIDs, 1, 2, 3)
}

// checklist: POP3-TOP-02 POP3-TOP-03
func TestOptionalTopFraming(t *testing.T) {
	for _, tc := range []struct {
		name, raw     string
		headers, body []string
	}{
		{"leading-dots", transactionMessages[0], []string{"Subject: a"}, []string{"..", "...", "..x"}},
		{"empty-headers", "\r\n.hidden\r\n", nil, []string{"..hidden"}},
		{"headers-only", "Subject: alone\r\n", []string{"Subject: alone"}, nil},
		{"blank-body-line", "Subject: blank\r\n\r\n\r\n.last\r\n", []string{"Subject: blank"}, []string{"", "..last"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := optional(t, []string{tc.raw}, []string{"opaque-top"})
			for _, n := range []int{0, 1, 2, 3, 100} {
				want := append(append([]string{}, tc.headers...), "")
				count := min(n, len(tc.body))
				want = append(want, tc.body[:count]...)
				top(t, w, fmt.Sprintf("TOP 1 %d", n), want...)
			}
		})
	}
}

// checklist: POP3-TOP-04 POP3-UIDL-03
func TestOptionalDeleteReset(t *testing.T) {
	w := optional(t, transactionMessages, updateUIDs)
	for range 2 {
		uidListing(t, w, updateUIDs, 1, 2, 3)
	}
	w.status(w.cmd("DELE 1"), "+OK")
	for range 2 {
		w.status(w.cmd("TOP 1 0"), "-ERR")
		w.status(w.cmd("UIDL 1"), "-ERR")
		uidListing(t, w, updateUIDs, 2, 3)
		top(t, w, "TOP 2 100", "Subject: second", "", "body")
	}
	for _, n := range []int{2, 3} {
		w.status(w.cmd(fmt.Sprintf("DELE %d", n)), "+OK")
	}
	uidListing(t, w, updateUIDs)
	w.status(w.cmd("RSET"), "+OK")
	for range 2 {
		uidListing(t, w, updateUIDs, 1, 2, 3)
		top(t, w, "TOP 1 100", "Subject: a", "", "..", "...", "..x")
	}
}

// checklist: POP3-UIDL-02 POP3-UIDL-04
func TestOptionalUIDLListing(t *testing.T) {
	// Include both printable ASCII endpoints and the 70-byte upper length bound.
	uids := []string{"!", "~", strings.Repeat("A", 70)}
	w := optional(t, transactionMessages, uids)
	for range 2 {
		uidListing(t, w, uids, 1, 2, 3)
	}
	empty := optional(t, nil, nil)
	for range 2 {
		uidListing(t, empty, nil)
	}
	empty.status(empty.cmd("UIDL 1"), "-ERR")
}

// checklist: POP3-UIDL-04
func TestOptionalUIDLInvalidBackend(t *testing.T) {
	// Existing fail-safe behavior, not a new runtime fix. Duplicate rejection is
	// a library policy; this does not claim the RFC forbids identical-copy hashes.
	for _, tc := range []struct {
		name, uid string
		duplicate bool
	}{
		{"empty", "", false}, {"too-long", strings.Repeat("x", 71), false},
		{"space", "bad id", false}, {"CRLF", "bad\r\n.", false},
		{"tab", "bad\tid", false}, {"NUL", "bad\x00id", false},
		{"DEL", "bad\x7fid", false}, {"non-ASCII", "badé", false},
		{"duplicate", "safe-first", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := optional(t, transactionMessages[:2], []string{"safe-first", tc.uid})
			if !tc.duplicate {
				w.status(w.cmd("UIDL 2"), "-ERR")
				w.status(w.cmd("NOOP"), "+OK")
			}
			w.status(w.cmd("UIDL"), "-ERR")
			w.status(w.cmd("NOOP"), "+OK")
			// Validation only considers visible messages; a bad/deleted handle must
			// neither poison nor appear in the otherwise valid listing.
			w.status(w.cmd("DELE 2"), "+OK")
			uidListing(t, w, []string{"safe-first", tc.uid}, 1)
		})
	}
}
