package rfctest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/server"
)

// remove takes a message out of the shared mailbox, as another client's
// EXPUNGE would.
func (m *sharedMailbox) remove(uid uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = slices.DeleteFunc(m.messages, func(msg imap.Message) bool { return msg.UID == uid })
}

// deliver adds a message to the shared mailbox, as new mail would.
func (m *sharedMailbox) deliver(uid uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, imap.Message{UID: uid})
}

// failingShared is a sharedMailbox whose reads can be made to fail.
type failingShared struct {
	*sharedMailbox
	fail bool
}

func (m *failingShared) Messages(folder string) ([]imap.Message, error) {
	if m.fail {
		return nil, errors.New("store unavailable")
	}
	return m.sharedMailbox.Messages(folder)
}

func sharedClient(t *testing.T, box imap.Mailbox) *client {
	t.Helper()
	c := selectionClient(t, box)
	_, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	return c
}

// TestNoopReportsOtherSourcesChanges: "A server MUST send mailbox size updates
// automatically if a mailbox size change is observed during the processing of
// a command. A server SHOULD send message flag updates automatically" (RFC 3501
// §5.2), and NOOP "can be used as a periodic poll for new messages or message
// status updates" (§6.1.2). NOOP re-reads the selected mailbox and reports
// another source's expunge, flag change and new mail; an expunge alone is never
// an EXISTS that reduces the count, and nothing changed or a failed read
// reports nothing.
//
// checklist: IMAP-UPDATE-01
// checklist: IMAP2-CORE-35
// checklist: IMAP2-CMD-172
func TestNoopReportsOtherSourcesChanges(t *testing.T) {
	shared := &sharedMailbox{messages: []imap.Message{{UID: 5001}, {UID: 5002}, {UID: 5003}}}
	box := &failingShared{sharedMailbox: shared}
	c := sharedClient(t, box)

	shared.remove(5001)
	shared.set(5002, func(msg *imap.Message) { msg.Flagged = true })
	shared.deliver(5004)
	lines, tagged := c.command("N1", "NOOP")
	requireOK(t, tagged, "N1")
	requireExactly(t, lines, "* 1 EXPUNGE", `* 1 FETCH (FLAGS (\Flagged))`, "* 3 EXISTS")
	lines, tagged = c.command("F1", "FETCH 1:* (UID)")
	requireOK(t, tagged, "F1")
	requireExactly(t, lines, `* 1 FETCH (UID 5002 FLAGS (\Flagged))`, "* 2 FETCH (UID 5003 FLAGS ())", "* 3 FETCH (UID 5004 FLAGS ())")

	lines, tagged = c.command("N2", "NOOP")
	requireOK(t, tagged, "N2")
	requireExactly(t, lines)

	shared.remove(5003)
	lines, tagged = c.command("N3", "NOOP")
	requireOK(t, tagged, "N3")
	requireExactly(t, lines, "* 2 EXPUNGE")

	box.fail = true
	shared.deliver(5005)
	lines, tagged = c.command("N4", "NOOP")
	requireOK(t, tagged, "N4")
	requireExactly(t, lines)
	box.fail = false
	lines, tagged = c.command("N5", "NOOP")
	requireOK(t, tagged, "N5")
	requireExactly(t, lines, "* 3 EXISTS")
}

// TestCheckReportsLikeNoop: "If a server implementation has no such
// housekeeping considerations, CHECK is equivalent to NOOP" (RFC 3501 §6.4.1),
// so CHECK reports another source's changes as NOOP does, under SELECT and
// EXAMINE.
//
// checklist: IMAP-UPDATE-02
func TestCheckReportsLikeNoop(t *testing.T) {
	for _, selection := range []string{"SELECT", "EXAMINE"} {
		t.Run(selection, func(t *testing.T) {
			shared := &sharedMailbox{messages: []imap.Message{{UID: 7}, {UID: 8}}}
			c := selectionClient(t, shared)
			_, tagged := c.command("S1", "%s INBOX", selection)
			requireOK(t, tagged, "S1")
			shared.remove(7)
			shared.set(8, func(msg *imap.Message) { msg.Seen = true })
			shared.deliver(9)
			lines, tagged := c.command("C1", "CHECK")
			requireOK(t, tagged, "C1")
			requireExactly(t, lines, "* 1 EXPUNGE", `* 1 FETCH (FLAGS (\Seen))`, "* 2 EXISTS")
		})
	}
}

// TestNoUntaggedResponseWithoutCommand: RFC 3501 §5.3 lets a server send
// untagged responses while no command is in progress, provided it deals with
// flow control. This server does not: another source's changes wait for the
// client's next command, even after an idle poll interval has passed, so no
// unsolicited write can block on a full transport. (The inactivity autologout
// BYE is written under a deadline.)
//
// checklist: IMAP-UPDATE-03
// checklist: IMAP2-CORE-37
// checklist: IMAP2-CMD-168
// checklist: IMAP2-RESP-199
func TestNoUntaggedResponseWithoutCommand(t *testing.T) {
	shared := &sharedMailbox{messages: []imap.Message{{UID: 1}}}
	c := dialConfigured(t, shared, nil, nil, func(s *server.Session) { s.SetIdlePollInterval(10 * time.Millisecond) })
	c.line()
	c.login()
	_, tagged := c.command("S1", "SELECT INBOX")
	requireOK(t, tagged, "S1")
	shared.deliver(2)

	if err := c.conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if b, err := c.r.ReadByte(); err == nil {
		t.Fatalf("the server wrote %q with no command in progress", b)
	}
	if err := c.conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	lines, tagged := c.command("N1", "NOOP")
	requireOK(t, tagged, "N1")
	requireExactly(t, lines, "* 2 EXISTS")
}

// TestPipelinedCommandsRunInOrder: a client may send commands without waiting
// (RFC 3501 §5.5), and "if the server detects a possible ambiguity, it MUST
// execute commands to completion in the order given by the client" — the RFC's
// example is a FETCH of a message's flags and a STORE of the same flags. The
// server runs every command to completion before reading the next, so the
// responses to FETCH + STORE + FETCH + SEARCH + CHECK, sent at once, come in
// order and each sees the one before.
//
// checklist: IMAP-UPDATE-04
// checklist: IMAP2-CORE-40
func TestPipelinedCommandsRunInOrder(t *testing.T) {
	shared := &sharedMailbox{messages: []imap.Message{{UID: 1}, {UID: 2}}}
	c := sharedClient(t, shared)
	commands := []string{"P1 FETCH 2 (FLAGS)", `P2 STORE 2 +FLAGS (\Seen)`, "P3 FETCH 2 (FLAGS)", "P4 SEARCH SEEN", "P5 CHECK"}
	if _, err := fmt.Fprint(c.conn, strings.Join(commands, "\r\n")+"\r\n"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"* 2 FETCH (UID 2 FLAGS ())", "P1 OK",
		`* 2 FETCH (FLAGS (\Seen))`, "P2 OK",
		`* 2 FETCH (UID 2 FLAGS (\Seen))`, "P3 OK",
		"* SEARCH 2", "P4 OK",
		"P5 OK",
	}
	for _, w := range want {
		if got := c.line(); !strings.HasPrefix(got, w) {
			t.Fatalf("pipelined response %q, want %q", got, w)
		}
	}
}
