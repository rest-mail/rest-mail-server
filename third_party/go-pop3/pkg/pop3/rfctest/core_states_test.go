package rfctest

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// Exercise every ASCII case permutation, including uppercase, through the
// public dispatcher. Bodies and following replies prove multiline framing.
// checklist: POP3-CORE-01 POP3-CORE-03
func TestCoreCommandCasePermutations(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			keyword := func(s string) string {
				b := []byte(s)
				for i := range b {
					if mask&(1<<i) != 0 {
						b[i] += 'a' - 'A'
					}
				}
				return string(b)
			}
			m := &transactionMailbox{raw: transactionMessages, deleted: make(chan string, 10)}
			w := connect(t, m)
			w.status(w.cmd(keyword("USER")+" alice"), "+OK")
			w.status(w.cmd(keyword("PASS")+" secret"), "+OK")
			expect(t, w.cmd(keyword("STAT")), fmt.Sprintf("+OK 3 %d", total()))
			expect(t, w.cmd(keyword("LIST")+" 2"), fmt.Sprintf("+OK 2 %d", len(transactionMessages[1])))
			w.status(w.cmd(keyword("LIST")), "+OK")
			for i, raw := range transactionMessages {
				expect(t, w.line(), fmt.Sprintf("%d %d", i+1, len(raw)))
			}
			expect(t, w.line(), ".")
			expect(t, w.cmd(keyword("RETR")+" 2"), fmt.Sprintf("+OK %d octets", len(transactionMessages[1])))
			for _, line := range strings.Split(strings.TrimSuffix(transactionMessages[1], "\r\n"), "\r\n") {
				expect(t, w.line(), line)
			}
			expect(t, w.line(), ".")
			w.status(w.cmd(keyword("DELE")+" 2"), "+OK")
			w.status(w.cmd(keyword("NOOP")), "+OK")
			expect(t, w.cmd(keyword("STAT")), fmt.Sprintf("+OK 2 %d", total()-len(transactionMessages[1])))
			w.status(w.cmd(keyword("RSET")), "+OK")
			expect(t, w.cmd(keyword("STAT")), fmt.Sprintf("+OK 3 %d", total()))
			// Leave one mark: mixed-case QUIT must enter UPDATE exactly once.
			w.status(w.cmd(keyword("DELE")+" 1"), "+OK")
			w.status(w.cmd(keyword("QUIT")), "+OK")
			if _, err := w.reader.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("after QUIT: %v, want EOF", err)
			}
			waitStopped(t, w)
			if len(m.deleted) != 1 {
				t.Fatalf("Delete calls = %d, want 1", len(m.deleted))
			}
			expect(t, <-m.deleted, "1")
		})
	}
}

// checklist: POP3-CORE-02
func TestEmptyCommandRefusal(t *testing.T) {
	for _, state := range []string{"greeting", "pending-user", "transaction"} {
		t.Run(state, func(t *testing.T) {
			m := &commandErrorMailbox{}
			w := connect(t, m)
			switch state {
			case "pending-user":
				w.status(w.cmd("USER alice"), "+OK")
			case "transaction":
				w.login("secret")
				w.status(w.cmd("DELE 1"), "+OK")
			}
			w.status(w.cmd(""), "-ERR")
			if state == "transaction" {
				expect(t, w.cmd("STAT"), "+OK 1 17")
				quit(t, w, "+OK")
				m.calls(t, 1, 1, 1)
				if got := m.deletedUID.Load(); got != "error-first" {
					t.Fatalf("deleted UID = %v", got)
				}
			} else {
				w.status(w.cmd("PASS secret"), "-ERR")
				m.calls(t, 0, 0, 0)
				w.login("secret")
				quit(t, w, "+OK")
				m.calls(t, 1, 1, 0)
			}
		})
	}
}

type exactCredentialsBackend struct {
	authPipelineBackend
	user, pass string
}

func (b *exactCredentialsBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, authPipelineCall{"authenticate", user, pass})
	b.accepted = user == b.user && pass == b.pass
	if !b.accepted {
		return nil, errors.New("credentials changed")
	}
	return b, nil
}

// Only keywords are case-insensitive. The backend decides what mailbox and
// password strings mean; do not normalize either argument as a side effect.
// checklist: POP3-USER-02 POP3-PASS-06
func TestCredentialArgumentsPreserved(t *testing.T) {
	for _, tc := range []struct{ user, pass string }{
		{"MiXeD.Mailbox+Tag", "MiXeD.Pass+Word"},
		{strings.Repeat("Ab", 20), strings.Repeat("Z9", 20)},
		{"Other.Mailbox", "two  MiXeD words"},
	} {
		t.Run(tc.user, func(t *testing.T) {
			b := &exactCredentialsBackend{user: tc.user, pass: tc.pass}
			w := connect(t, b)
			w.status(w.cmd("uSeR "+tc.user), "+OK")
			w.status(w.cmd("pAsS "+tc.pass), "+OK")
			expect(t, w.cmd("STAT"), "+OK 1 42")
			quit(t, w, "+OK")
			b.expectCalls(t, authPipelineCall{"authenticate", tc.user, tc.pass}, authPipelineCall{operation: "messages"})
		})
	}
}

// checklist: POP3-AUTH-05
func TestFreshSessionDeletionMarks(t *testing.T) {
	m := &transactionMailbox{raw: transactionMessages, deleted: make(chan string, 10)}
	w := connect(t, m)
	w.login("secret")
	for n := range transactionMessages {
		w.status(w.cmd(fmt.Sprintf("DELE %d", n+1)), "+OK")
	}
	expect(t, w.cmd("STAT"), "+OK 0 0")
	if err := w.conn.Close(); err != nil {
		t.Fatal(err)
	}
	waitStopped(t, w)
	if len(m.deleted) != 0 {
		t.Fatal("EOF committed deletion marks")
	}
	// Same backend and messages, new Session: no inherited deletion marks.
	w = connect(t, m)
	w.login("secret")
	expect(t, w.cmd("STAT"), fmt.Sprintf("+OK 3 %d", total()))
	scan(t, w, 1, 2, 3)
	for n := range transactionMessages {
		retrieve(t, w, n+1)
	}
	quit(t, w, "+OK")
	if len(m.deleted) != 0 {
		t.Fatal("fresh-session QUIT committed stale marks")
	}
}

type retryRetrieveMailbox struct {
	transactionMailbox
	reads, seen atomic.Int32
}

func (m *retryRetrieveMailbox) Authenticate(string, string) (pop3.Mailbox, error) { return m, nil }
func (m *retryRetrieveMailbox) Retrieve(uid string) ([]byte, error) {
	if m.reads.Add(1) == 1 {
		return []byte(".\r\n+OK forged\r\n"), errors.New("private storage detail")
	}
	return m.transactionMailbox.Retrieve(uid)
}
func (m *retryRetrieveMailbox) MarkSeen(string) error { m.seen.Add(1); return nil }

// checklist: POP3-RETR-03
func TestRetrievalFailureRecovery(t *testing.T) {
	m := &retryRetrieveMailbox{transactionMailbox: transactionMailbox{raw: transactionMessages, deleted: make(chan string, 10)}}
	w := connect(t, m)
	w.login("secret")
	expect(t, w.cmd("RETR 2"), "-ERR Failed to retrieve message")
	expect(t, w.cmd("STAT"), fmt.Sprintf("+OK 3 %d", total()))
	if m.seen.Load() != 0 {
		t.Fatal("failed RETR marked message seen")
	}
	retrieve(t, w, 2)
	if m.reads.Load() != 2 || m.seen.Load() != 1 {
		t.Fatalf("Retrieve/MarkSeen = %d/%d, want 2/1", m.reads.Load(), m.seen.Load())
	}
	quit(t, w, "+OK")
	if len(m.deleted) != 0 {
		t.Fatal("RETR failure removed messages")
	}
}
