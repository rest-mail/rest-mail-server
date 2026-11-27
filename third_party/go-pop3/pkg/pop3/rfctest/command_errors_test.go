package rfctest

import (
	"errors"
	"sync/atomic"
	"testing"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// Unknown keywords have RFC 1939's three/four-letter shape. APOP and AUTH
// are optional authentication commands that these fixtures do not handle: APOP
// is not implemented, and AUTH needs a SASLBackend (see sasl_test.go).
// Mixed-case controls exercise dispatch without claiming APOP/SASL support.
var unsupportedCommands = []struct{ name, command string }{
	{"unknown-three", "ZZZ"},
	{"unknown-four-argument", "xYzZ argument"},
	{"apop", "APOP alice c4c9334bac560ecc979e58001b3e22fb"},
	{"apop-mixed-case", "aPoP alice c4c9334bac560ecc979e58001b3e22fb"},
	{"auth", "AUTH PLAIN"},
	{"auth-mixed-case", "aUtH PLAIN"},
}

// Atomic counters observe every backend operation while Session.Handle runs.
// All data is in memory; opaque UIDs distinguish backend deletes from numbers.
type commandErrorMailbox struct {
	auth, messages, retrieve, seen, deleted atomic.Int32
	deletedUID                              atomic.Value
}

func (m *commandErrorMailbox) Authenticate(user, pass string) (pop3.Mailbox, error) {
	m.auth.Add(1)
	if user != "alice" || pass != "secret" {
		return nil, errors.New("invalid credentials")
	}
	return m, nil
}
func (m *commandErrorMailbox) Messages() ([]pop3.Message, error) {
	m.messages.Add(1)
	return []pop3.Message{{UID: "error-first", Size: 42}, {UID: "error-second", Size: 17}}, nil
}
func (m *commandErrorMailbox) Retrieve(string) ([]byte, error) {
	m.retrieve.Add(1)
	return nil, errors.New("unexpected retrieval")
}
func (m *commandErrorMailbox) MarkSeen(string) error { m.seen.Add(1); return nil }
func (m *commandErrorMailbox) Delete(uid string) error {
	m.deletedUID.Store(uid)
	m.deleted.Add(1)
	return nil
}
func (m *commandErrorMailbox) calls(t *testing.T, auth, messages, deleted int32) {
	t.Helper()
	got := [5]int32{m.auth.Load(), m.messages.Load(), m.retrieve.Load(), m.seen.Load(), m.deleted.Load()}
	want := [5]int32{auth, messages, 0, 0, deleted}
	if got != want {
		t.Fatalf("backend auth/messages/retrieve/seen/delete = %v, want %v", got, want)
	}
}

func commandErrorCapa(t *testing.T, w *wire) {
	t.Helper()
	expect(t, w.cmd("CAPA"), "+OK Capability list follows")
	// Pin the existing nil-TLS list and its terminator as a framing probe.
	// This adds no capability or new authentication classification.
	for _, line := range []string{"USER", "TOP", "UIDL", "RESP-CODES", "AUTH-RESP-CODE", "PIPELINING", "."} {
		expect(t, w.line(), line)
	}
}

// The RFC requires uppercase -ERR and CRLF, checked by exact status text and
// wire.line; explanatory prose is the current library's regression contract.
// checklist: POP3-COMMAND-01 POP3-COMMAND-02 POP3-COMMAND-POLICY-01
func TestCommandErrorsAuthorization(t *testing.T) {
	for _, tc := range unsupportedCommands {
		t.Run(tc.name, func(t *testing.T) {
			for _, pendingUser := range []bool{false, true} {
				t.Run(map[bool]string{false: "greeting", true: "after-user"}[pendingUser], func(t *testing.T) {
					m := &commandErrorMailbox{}
					w := connect(t, m)
					if pendingUser {
						expect(t, w.cmd("USER alice"), "+OK")
					}
					expect(t, w.cmd(tc.command), "-ERR Unknown command")
					m.calls(t, 0, 0, 0)
					commandErrorCapa(t, w)
					expect(t, w.cmd("STAT"), "-ERR Not authenticated")
					expect(t, w.cmd("NOOP"), "-ERR Not authenticated")
					m.calls(t, 0, 0, 0)
					// Start a fresh USER/PASS exchange: this does not assert strict
					// immediate-PASS sequencing or preservation of pending USER.
					w.login("secret")
					expect(t, w.cmd("STAT"), "+OK 2 59")
					quit(t, w, "+OK")
					m.calls(t, 1, 1, 0)
				})
			}
		})
	}
}

// Both QUIT and RSET controls distinguish preserved marks from a refusal that
// silently commits, clears, adds or moves them. No backend retrieval is needed.
// checklist: POP3-COMMAND-01 POP3-COMMAND-02 POP3-COMMAND-POLICY-01
func TestCommandErrorsTransaction(t *testing.T) {
	for _, tc := range unsupportedCommands {
		t.Run(tc.name, func(t *testing.T) {
			for _, reset := range []bool{false, true} {
				t.Run(map[bool]string{false: "commit", true: "reset"}[reset], func(t *testing.T) {
					m := &commandErrorMailbox{}
					w := connect(t, m)
					w.login("secret")
					expect(t, w.cmd("DELE 1"), "+OK Message 1 deleted")
					m.calls(t, 1, 1, 0)
					expect(t, w.cmd(tc.command), "-ERR Unknown command")
					m.calls(t, 1, 1, 0)
					commandErrorCapa(t, w)
					expect(t, w.cmd("STAT"), "+OK 1 17")
					expect(t, w.cmd("LIST 1"), "-ERR Message is deleted")
					expect(t, w.cmd("LIST 2"), "+OK 2 17")
					expect(t, w.cmd("NOOP"), "+OK")
					m.calls(t, 1, 1, 0)
					if reset {
						expect(t, w.cmd("RSET"), "+OK Maildrop has 2 messages")
						expect(t, w.cmd("STAT"), "+OK 2 59")
						expect(t, w.cmd("LIST 1"), "+OK 1 42")
					}
					quit(t, w, "+OK")
					if reset {
						m.calls(t, 1, 1, 0)
					} else {
						m.calls(t, 1, 1, 1)
						if got := m.deletedUID.Load(); got != "error-first" {
							t.Fatalf("QUIT deleted UID = %v, want error-first", got)
						}
					}
				})
			}
		})
	}
}
