package rfctest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// Canonical CRLF fixtures keep expected sizes independent of OctetCount. The
// first reuses the leading-dot cases from the existing root octet corpus.
var transactionMessages = []string{
	"Subject: a\r\n\r\n.\r\n..\r\n.x\r\n",
	"Subject: second\r\n\r\nbody\r\n",
	"Third\r\n",
}

type transactionMailbox struct {
	raw     []string
	deleted chan string
}

func (m *transactionMailbox) Authenticate(string, string) (pop3.Mailbox, error) { return m, nil }
func (m *transactionMailbox) Messages() ([]pop3.Message, error) {
	msgs := make([]pop3.Message, len(m.raw))
	for i, raw := range m.raw {
		msgs[i] = pop3.Message{UID: fmt.Sprint(i + 1), Size: len(raw)}
	}
	return msgs, nil
}
func (m *transactionMailbox) Retrieve(uid string) ([]byte, error) {
	for i, raw := range m.raw {
		if uid == fmt.Sprint(i+1) {
			return []byte(raw), nil
		}
	}
	return nil, errors.New("unknown UID")
}
func (*transactionMailbox) MarkSeen(string) error     { return nil }
func (m *transactionMailbox) Delete(uid string) error { m.deleted <- uid; return nil }

func transaction(t *testing.T, raw []string) (*wire, *transactionMailbox) {
	t.Helper()
	m := &transactionMailbox{raw: raw, deleted: make(chan string, 10)}
	w := connect(t, m)
	w.login("secret")
	return w, m
}
func expect(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
}
func scan(t *testing.T, w *wire, numbers ...int) {
	t.Helper()
	w.status(w.cmd("LIST"), "+OK")
	for _, n := range numbers {
		expect(t, w.line(), fmt.Sprintf("%d %d", n, len(transactionMessages[n-1])))
	}
	expect(t, w.line(), ".")
	// A new response detects an extra scan line or an unconsumed terminator.
	w.status(w.cmd("NOOP"), "+OK")
}
func retrieve(t *testing.T, w *wire, n int) {
	t.Helper()
	raw := transactionMessages[n-1]
	expect(t, w.cmd(fmt.Sprintf("RETR %d", n)), fmt.Sprintf("+OK %d octets", len(raw)))
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\r\n"), "\r\n") {
		if strings.HasPrefix(line, ".") {
			line = "." + line
		}
		expect(t, w.line(), line)
	}
	expect(t, w.line(), ".")
	w.status(w.cmd("NOOP"), "+OK")
}
func total() int {
	n := 0
	for _, raw := range transactionMessages {
		n += len(raw)
	}
	return n
}

// checklist: POP3-STAT-01 POP3-LIST-01 POP3-LIST-02 POP3-LIST-03
func TestTransactionStatList(t *testing.T) {
	w, _ := transaction(t, transactionMessages)
	for range 2 {
		expect(t, w.cmd("STAT"), fmt.Sprintf("+OK 3 %d", total()))
		scan(t, w, 1, 2, 3)
		for n, raw := range transactionMessages {
			expect(t, w.cmd(fmt.Sprintf("LIST %d", n+1)), fmt.Sprintf("+OK %d %d", n+1, len(raw)))
		}
	}
}

// checklist: POP3-STAT-01 POP3-LIST-02
func TestTransactionEmptyMaildrop(t *testing.T) {
	w, _ := transaction(t, nil)
	expect(t, w.cmd("STAT"), "+OK 0 0")
	scan(t, w)
}

// checklist: POP3-RETR-01
func TestTransactionRetrieval(t *testing.T) {
	w, _ := transaction(t, transactionMessages)
	for range 2 {
		for n := range transactionMessages {
			retrieve(t, w, n+1)
		}
	}
}

// checklist: POP3-TRANS-01 POP3-TRANS-02 POP3-LIST-03 POP3-RETR-02 POP3-DELE-01
func TestTransactionArguments(t *testing.T) {
	m := &transactionMailbox{raw: transactionMessages, deleted: make(chan string, 10)}
	w := connect(t, m)
	for _, cmd := range []string{"STAT", "LIST", "RETR 1", "DELE 1", "NOOP", "RSET"} {
		w.status(w.cmd(cmd), "-ERR")
	}
	w.login("secret")
	for _, cmd := range []string{"STAT 1", "NOOP 1", "RSET 1", "RETR", "DELE"} {
		w.status(w.cmd(cmd), "-ERR")
	}
	for _, cmd := range []string{"LIST", "RETR", "DELE"} {
		for _, arg := range []string{"0", "-1", "4", "nonsense", "1 2"} {
			w.status(w.cmd(cmd+" "+arg), "-ERR")
		}
	}
	expect(t, w.cmd("STAT"), fmt.Sprintf("+OK 3 %d", total()))
	scan(t, w, 1, 2, 3)
}

// checklist: POP3-STAT-02 POP3-LIST-01 POP3-LIST-03 POP3-RETR-02 POP3-DELE-01 POP3-RSET-01
func TestTransactionDeleteReset(t *testing.T) {
	w, m := transaction(t, transactionMessages)
	for _, n := range []int{1, 2} {
		w.status(w.cmd(fmt.Sprintf("DELE %d", n)), "+OK")
	}
	expect(t, w.cmd("STAT"), fmt.Sprintf("+OK 1 %d", len(transactionMessages[2])))
	scan(t, w, 3) // Remaining message keeps number 3, not 1.
	for _, n := range []int{1, 2} {
		for _, cmd := range []string{"LIST", "RETR", "DELE"} {
			w.status(w.cmd(fmt.Sprintf("%s %d", cmd, n)), "-ERR")
		}
	}
	if len(m.deleted) != 0 {
		t.Fatal("DELE physically removed a message before UPDATE")
	}
	w.status(w.cmd("RSET"), "+OK")
	expect(t, w.cmd("STAT"), fmt.Sprintf("+OK 3 %d", total()))
	scan(t, w, 1, 2, 3)
	for n := range transactionMessages {
		retrieve(t, w, n+1)
	}
	// Restore all marks even when the visible maildrop becomes empty.
	for n := range transactionMessages {
		w.status(w.cmd(fmt.Sprintf("DELE %d", n+1)), "+OK")
	}
	expect(t, w.cmd("STAT"), "+OK 0 0")
	scan(t, w)
	for range 2 {
		w.status(w.cmd("RSET"), "+OK")
	}
	expect(t, w.cmd("STAT"), fmt.Sprintf("+OK 3 %d", total()))
	scan(t, w, 1, 2, 3)
	if len(m.deleted) != 0 {
		t.Fatal("RSET physically removed a message")
	}
}

// checklist: POP3-NOOP-01
func TestTransactionNoop(t *testing.T) {
	w, m := transaction(t, transactionMessages)
	w.status(w.cmd("DELE 2"), "+OK")
	for range 2 {
		w.status(w.cmd("NOOP"), "+OK")
	}
	expect(t, w.cmd("STAT"), fmt.Sprintf("+OK 2 %d", total()-len(transactionMessages[1])))
	scan(t, w, 1, 3)
	w.status(w.cmd("RETR 2"), "-ERR")
	retrieve(t, w, 1)
	retrieve(t, w, 3)
	if len(m.deleted) != 0 {
		t.Fatal("NOOP physically removed a message")
	}
}
