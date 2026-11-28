package rfctest

import (
	"bufio"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// A backend can discover contention while authenticating or while opening the
// authenticated maildrop. Non-nil partial results must be ignored on error.
type inUseBackend struct {
	commandErrorMailbox
	busyErr    error
	duringAuth bool
}

func (b *inUseBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	m, err := b.commandErrorMailbox.Authenticate(user, pass)
	if err != nil {
		return nil, err
	}
	if b.duringAuth && b.auth.Load() <= 2 {
		return m, b.busyErr
	}
	return b, nil
}

func (b *inUseBackend) Messages() ([]pop3.Message, error) {
	if !b.duringAuth && b.auth.Load() <= 2 {
		b.messages.Add(1)
		return []pop3.Message{{UID: "partial", Size: 999}}, b.busyErr
	}
	return b.commandErrorMailbox.Messages()
}

type inUseLimiter struct {
	pop3.NopLimiter
	fails, bans, resets atomic.Int32
}

func (l *inUseLimiter) RecordAuthFail(string) { l.fails.Add(1) }
func (l *inUseLimiter) IsBanned(string) bool {
	l.bans.Add(1)
	return l.fails.Load() != 0
}
func (l *inUseLimiter) ResetAuth(string) { l.resets.Add(1) }

func (l *inUseLimiter) check(t *testing.T, resets int32) {
	t.Helper()
	if got := [3]int32{l.fails.Load(), l.bans.Load(), l.resets.Load()}; got != [3]int32{0, 0, resets} {
		t.Fatalf("limiter failures/ban checks/resets = %v, want [0 0 %d]", got, resets)
	}
}

// RFC 2449 §8.1.2 defines IN-USE for successful authentication followed by
// contention. These are backend-signalled failures, not an engine lock proof.
// checklist: POP3-RESP-CODES-05
func TestResponseCodeInUse(t *testing.T) {
	t.Run("Backend", func(t *testing.T) {
		testResponseCodeInUse(t, false)
	})
	// AuthenticateUTF8 must receive the same response code and limiter
	// treatment, while preserving UTF8 mode through subsequent retries.
	t.Run("UTF8Backend", func(t *testing.T) {
		testResponseCodeInUse(t, true)
	})
}

type utf8InUseBackend struct{ *inUseBackend }

func (b *utf8InUseBackend) Authenticate(_, _ string) (pop3.Mailbox, error) {
	return nil, fmt.Errorf("Authenticate called instead of AuthenticateUTF8")
}

func (b *utf8InUseBackend) AuthenticateUTF8(user, pass string, utf8 bool) (pop3.Mailbox, error) {
	if !utf8 {
		return nil, fmt.Errorf("UTF8 mode lost")
	}
	return b.inUseBackend.Authenticate(user, pass)
}

func testResponseCodeInUse(t *testing.T, utf8 bool) {
	t.Helper()
	for _, duringAuth := range []bool{true, false} {
		for _, wrapped := range []bool{false, true} {
			for _, retry := range []bool{false, true} {
				name := fmt.Sprintf("authenticate=%t/wrapped=%t/retry=%t", duringAuth, wrapped, retry)
				t.Run(name, func(t *testing.T) {
					busyErr := pop3.ErrInUse
					if wrapped {
						busyErr = fmt.Errorf("private backend lock detail: %w", busyErr)
					}
					b := &inUseBackend{busyErr: busyErr, duringAuth: duringAuth}
					var backend pop3.Backend = b
					if utf8 {
						backend = &utf8InUseBackend{b}
					}
					l := &inUseLimiter{}
					client, server := net.Pipe()
					done := make(chan struct{})
					go func() {
						defer close(done)
						pop3server.NewSession(server, backend, nil, l).Handle()
					}()
					t.Cleanup(func() {
						_ = client.Close()
						select {
						case <-done:
						case <-time.After(5 * time.Second):
							t.Error("session did not stop")
						}
					})
					w := &wire{t: t, conn: client, reader: bufio.NewReader(client), done: done}
					expect(t, w.line(), "+OK POP3 server ready")
					if utf8 {
						expect(t, w.cmd("CAPA"), "+OK Capability list follows")
						for _, line := range []string{"USER", "TOP", "UIDL", "RESP-CODES", "AUTH-RESP-CODE", "PIPELINING", "UTF8", "."} {
							expect(t, w.line(), line)
						}
						expect(t, w.cmd("UTF8"), "+OK UTF8 enabled")
					} else {
						commandErrorCapa(t, w)
					}
					var messages int32
					for attempt := int32(1); attempt <= 2; attempt++ {
						expect(t, w.cmd("USER alice"), "+OK")
						// Pin code/framing and ensure private error details never leak.
						expect(t, w.cmd("PASS secret"), "-ERR [IN-USE] Maildrop already in use")
						if !duringAuth {
							messages++
						}
						for _, command := range []string{"PASS secret", "STAT", "LIST", "UIDL", "RETR 1", "TOP 1 0", "DELE 1", "RSET", "NOOP"} {
							w.status(w.cmd(command), "-ERR")
						}
						b.calls(t, attempt, messages, 0)
						l.check(t, attempt)
					}
					if retry {
						w.login("secret")
						expect(t, w.cmd("STAT"), "+OK 2 59")
						expect(t, w.cmd("LIST 1"), "+OK 1 42")
						expect(t, w.cmd("LIST 2"), "+OK 2 17")
						expect(t, w.cmd("DELE 1"), "+OK Message 1 deleted")
						quit(t, w, "+OK")
						b.calls(t, 3, messages+1, 1)
						if uid := b.deletedUID.Load(); uid != "error-first" {
							t.Fatalf("deleted UID = %v, want error-first", uid)
						}
						l.check(t, 3)
					} else {
						quit(t, w, "+OK")
						b.calls(t, 2, messages, 0)
						l.check(t, 2)
					}
				})
			}
		}
	}
}
