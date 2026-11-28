package rfctest

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// statusCase fails unless line starts with exactly "+OK" or "-ERR", in upper
// case, as the whole line or followed by a space (RFC 1939 §3).
func statusCase(t *testing.T, line string) {
	t.Helper()
	for _, s := range []string{"+OK", "-ERR"} {
		if line == s || strings.HasPrefix(line, s+" ") {
			return
		}
	}
	t.Fatalf("response %q does not start with upper-case +OK or -ERR", line)
}

// closed fails unless the server closed the connection after its last reply.
func (w *wire) closed() {
	w.t.Helper()
	_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if line, err := w.reader.ReadString('\n'); !errors.Is(err, io.EOF) {
		w.t.Fatalf("read after close = %q, %v; want EOF", line, err)
	}
}

// Every first response line in both states, positive and negative, carries
// the status indicator in upper case. The drop and scan listings carry
// nothing after the size, as RFC 1939 §5 asks of minimal implementations.
// checklist: POP3-STATUS-01 POP3-STATUS-02
func TestRFC1939StatusIndicatorCase(t *testing.T) {
	m := &commandErrorMailbox{}
	w := connect(t, m) // connect checks the greeting's +OK
	steps := []struct{ command, want string }{
		{"NOOP", "-ERR"},
		{"STAT", "-ERR"},
		{"USER", "-ERR"},
		{"PASS secret", "-ERR"},
		{"USER alice", "+OK"},
		{"PASS wrong", "-ERR"},
		{"USER alice", "+OK"},
		{"PASS secret", "+OK"},
		{"STAT", "+OK"},
		{"LIST 2", "+OK"},
		{"LIST 3", "-ERR"},
		{"UIDL 1", "+OK"},
		{"DELE 1", "+OK"},
		{"DELE 1", "-ERR"},
		{"RSET", "+OK"},
		{"NOOP", "+OK"},
		{"USER alice", "-ERR"},
		{"nOoP", "+OK"},
	}
	for _, step := range steps {
		got := w.cmd(step.command)
		statusCase(t, got)
		w.status(got, step.want)
	}
	expect(t, w.cmd("STAT"), "+OK 2 59")
	expect(t, w.cmd("LIST 2"), "+OK 2 17")
	statusCase(t, w.cmd("LIST"))
	for _, line := range []string{"1 42", "2 17", "."} {
		expect(t, w.line(), line)
	}
	got := w.cmd("QUIT")
	statusCase(t, got)
	w.status(got, "+OK")
	w.closed()
	m.calls(t, 2, 1, 0)
}

// Each TRANSACTION command before login, and each AUTHORIZATION command after
// it, gets a negative reply with no backend effect; the session stays usable.
// checklist: POP3-STATE-01
func TestRFC1939WrongStateMatrix(t *testing.T) {
	transactionOnly := []string{"STAT", "LIST", "LIST 1", "RETR 1", "DELE 1", "NOOP", "RSET", "TOP 1 0", "UIDL", "UIDL 1"}
	for _, command := range transactionOnly {
		for _, pendingUser := range []bool{false, true} {
			name := command + map[bool]string{false: "/greeting", true: "/after-user"}[pendingUser]
			t.Run(name, func(t *testing.T) {
				m := &commandErrorMailbox{}
				w := connect(t, m)
				if pendingUser {
					expect(t, w.cmd("USER alice"), "+OK")
				}
				w.status(w.cmd(command), "-ERR")
				m.calls(t, 0, 0, 0)
				w.login("secret")
				expect(t, w.cmd("STAT"), "+OK 2 59")
				m.calls(t, 1, 1, 0)
			})
		}
	}
	for _, command := range []string{"USER alice", "USER bob", "PASS secret", "PASS wrong"} {
		t.Run("transaction/"+command, func(t *testing.T) {
			m := &commandErrorMailbox{}
			w := connect(t, m)
			w.login("secret")
			w.status(w.cmd("DELE 2"), "+OK")
			w.status(w.cmd(command), "-ERR")
			expect(t, w.cmd("STAT"), "+OK 1 42")
			w.status(w.cmd("QUIT"), "+OK")
			w.closed()
			m.calls(t, 1, 1, 1)
			if uid := m.deletedUID.Load(); uid != "error-second" {
				t.Fatalf("deleted UID = %v, want error-second", uid)
			}
		})
	}
}

// USER/PASS works with every Backend. A negative USER reply leaves the client
// free to start again or QUIT, and USER does not consult the backend, so its
// reply never tells an unknown mailbox from a known one.
// checklist: POP3-MECH-01 POP3-USER-03 POP3-USER-04
func TestRFC1939UserNegativeThenRetry(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		m := &commandErrorMailbox{}
		w := connect(t, m)
		w.status(w.cmd("USER"), "-ERR")
		w.login("secret")
		expect(t, w.cmd("STAT"), "+OK 2 59")
		m.calls(t, 1, 1, 0)
	})
	t.Run("quit", func(t *testing.T) {
		m := &commandErrorMailbox{}
		w := connect(t, m)
		w.status(w.cmd("USER"), "-ERR")
		w.status(w.cmd("QUIT"), "+OK")
		w.closed()
		m.calls(t, 0, 0, 0)
	})
	t.Run("unknown-mailbox", func(t *testing.T) {
		m := &commandErrorMailbox{}
		w := connect(t, m)
		known, unknown := w.cmd("USER alice"), w.cmd("USER nobody")
		if known != unknown {
			t.Fatalf("USER replies differ: known %q, unknown %q", known, unknown)
		}
		w.status(unknown, "+OK")
		m.calls(t, 0, 0, 0)
		w.status(w.cmd("PASS secret"), "-ERR")
		m.calls(t, 1, 0, 0)
		w.login("secret")
		m.calls(t, 2, 1, 0)
	})
}
