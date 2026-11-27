package rfctest

import (
	"bufio"
	"fmt"
	"net"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// A backend that enforces a login delay refuses valid credentials until the
// delay expires. Non-nil partial results must be ignored on error.
type loginDelayBackend struct {
	commandErrorMailbox
	delayErr error
}

func (b *loginDelayBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	m, err := b.commandErrorMailbox.Authenticate(user, pass)
	if err != nil {
		return nil, err
	}
	if b.auth.Load() <= 2 {
		return m, b.delayErr
	}
	return b, nil
}

// RFC 2449 §8.1.1 defines LOGIN-DELAY for a user who logged in too recently.
// The delay is backend-signalled; the engine only reports it.
// checklist: POP3-RESP-CODES-06
func TestResponseCodeLoginDelay(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, retry := range []bool{false, true} {
			name := fmt.Sprintf("wrapped=%t/retry=%t", wrapped, retry)
			t.Run(name, func(t *testing.T) {
				delayErr := pop3.ErrLoginDelay
				if wrapped {
					delayErr = fmt.Errorf("private backend timing detail: %w", delayErr)
				}
				b := &loginDelayBackend{delayErr: delayErr}
				l := &inUseLimiter{}
				client, server := net.Pipe()
				done := make(chan struct{})
				go func() {
					defer close(done)
					pop3server.NewSession(server, b, nil, l).Handle()
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
				commandErrorCapa(t, w)
				for attempt := int32(1); attempt <= 2; attempt++ {
					// USER never reveals the delay (RFC 2449 §8.1.1 note).
					expect(t, w.cmd("USER alice"), "+OK")
					// Pin code/framing and ensure private error details never leak.
					expect(t, w.cmd("PASS secret"), "-ERR [LOGIN-DELAY] Logged in too recently")
					for _, command := range []string{"PASS secret", "STAT", "LIST", "UIDL", "RETR 1", "TOP 1 0", "DELE 1", "RSET", "NOOP"} {
						w.status(w.cmd(command), "-ERR")
					}
					b.calls(t, attempt, 0, 0)
					l.check(t, attempt)
				}
				if retry {
					w.login("secret")
					expect(t, w.cmd("STAT"), "+OK 2 59")
					expect(t, w.cmd("DELE 1"), "+OK Message 1 deleted")
					quit(t, w, "+OK")
					b.calls(t, 3, 1, 1)
					if uid := b.deletedUID.Load(); uid != "error-first" {
						t.Fatalf("deleted UID = %v, want error-first", uid)
					}
					l.check(t, 3)
				} else {
					quit(t, w, "+OK")
					b.calls(t, 2, 0, 0)
					l.check(t, 2)
				}
			})
		}
	}
}
