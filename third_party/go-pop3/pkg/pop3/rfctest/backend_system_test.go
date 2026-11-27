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

// Authentication infrastructure can fail before it verifies credentials. A
// maildrop can fail after successful authentication. Discard partial results.
type systemFailureBackend struct {
	commandErrorMailbox
	systemErr  error
	duringAuth bool
}

func (b *systemFailureBackend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if b.duringAuth && b.auth.Load() < 2 {
		b.auth.Add(1)
		return b, b.systemErr
	}
	if _, err := b.commandErrorMailbox.Authenticate(user, pass); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *systemFailureBackend) Messages() ([]pop3.Message, error) {
	if !b.duringAuth && b.auth.Load() <= 2 {
		b.messages.Add(1)
		return []pop3.Message{{UID: "partial", Size: 999}}, b.systemErr
	}
	return b.commandErrorMailbox.Messages()
}

// RFC 3206 §4 distinguishes transient and intervention-required system errors
// from credential failures, including when credentials cannot be checked.
// checklist: POP3-RESP-CODES-07 POP3-RESP-CODES-10
func TestResponseCodeBackendSystemFailures(t *testing.T) {
	for _, tc := range []struct {
		code string
		err  error
	}{
		{"SYS/TEMP", pop3.ErrSystemTemporary},
		{"SYS/PERM", pop3.ErrSystemPermanent},
	} {
		for _, duringAuth := range []bool{true, false} {
			for _, wrapped := range []bool{false, true} {
				for _, retry := range []bool{false, true} {
					name := fmt.Sprintf("%s/authenticate=%t/wrapped=%t/retry=%t", tc.code, duringAuth, wrapped, retry)
					t.Run(name, func(t *testing.T) {
						systemErr := tc.err
						if wrapped {
							systemErr = fmt.Errorf("private backend infrastructure detail: %w", systemErr)
						}
						b := &systemFailureBackend{systemErr: systemErr, duringAuth: duringAuth}
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
						var messages int32
						for attempt := int32(1); attempt <= 2; attempt++ {
							username, password := "alice", "secret"
							if duringAuth {
								// The unavailable service cannot verify this account.
								username, password = "unknown", "wrong"
							}
							expect(t, w.cmd("USER "+username), "+OK")
							message := "Failed to load mailbox"
							if duringAuth {
								message = "Authentication unavailable"
							}
							// Exact response also proves private error details never leak.
							expect(t, w.cmd("PASS "+password), "-ERR ["+tc.code+"] "+message)
							if !duringAuth {
								messages++
							}
							for _, command := range []string{"PASS secret", "STAT", "LIST", "UIDL", "RETR 1", "TOP 1 0", "DELE 1", "RSET", "NOOP"} {
								w.status(w.cmd(command), "-ERR")
							}
							b.calls(t, attempt, messages, 0)
							resets := attempt
							if duringAuth {
								resets = 0
							}
							l.check(t, resets)
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
							resets := int32(3)
							if duringAuth {
								resets = 1
							}
							l.check(t, resets)
						} else {
							quit(t, w, "+OK")
							b.calls(t, 2, messages, 0)
							resets := int32(2)
							if duringAuth {
								resets = 0
							}
							l.check(t, resets)
						}
					})
				}
			}
		}
	}
}
