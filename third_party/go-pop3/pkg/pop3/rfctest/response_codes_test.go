package rfctest

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// checklist: POP3-RESP-CODES-01
func TestResponseCodeCapabilities(t *testing.T) {
	w := connect(t, backend{password: "secret"})
	for _, authenticated := range []bool{false, true} {
		if authenticated {
			w.login("secret")
		}
		w.status(w.cmd("CAPA"), "+OK")
		count := 0
		for i := 0; ; i++ {
			if i > 32 {
				t.Fatal("CAPA missing terminator")
			}
			line := w.line()
			if line == "." {
				break
			}
			tag, _, _ := strings.Cut(line, " ")
			if strings.EqualFold(tag, "RESP-CODES") {
				expect(t, line, "RESP-CODES")
				count++
			}
		}
		if count != 1 {
			t.Fatalf("CAPA authenticated=%t: RESP-CODES count = %d, want 1", authenticated, count)
		}
		if authenticated {
			expect(t, w.cmd("STAT"), "+OK 1 42")
		} else {
			expect(t, w.cmd("STAT"), "-ERR Not authenticated")
		}
	}
}

// checklist: POP3-RESP-CODES-02 POP3-RESP-POLICY-01
func TestResponseCodeCredentialFailures(t *testing.T) {
	w := connect(t, backend{password: "secret"})
	for _, credentials := range [][2]string{{"alice", "wrong"}, {"unknown", "secret"}} {
		// USER does not distinguish an existing account from an unknown one.
		expect(t, w.cmd("USER "+credentials[0]), "+OK")
		// Exact equality pins the status separator, brackets, AUTH token and
		// following space; wire.line also checks the actual CRLF framing.
		expect(t, w.cmd("PASS "+credentials[1]), "-ERR [AUTH] Authentication failed")
	}
}

// checklist: POP3-RESP-POLICY-02
func TestResponseCodeAuthenticationRetry(t *testing.T) {
	for _, user := range []string{"alice", "unknown"} {
		t.Run(user, func(t *testing.T) {
			w := connect(t, backend{password: "secret"})
			expect(t, w.cmd("USER "+user), "+OK")
			expect(t, w.cmd("PASS wrong"), "-ERR [AUTH] Authentication failed")
			// Even correct credentials cannot reuse the failed attempt's USER.
			expect(t, w.cmd("PASS secret"), "-ERR USER first")
			expect(t, w.cmd("STAT"), "-ERR Not authenticated")
			expect(t, w.cmd("USER alice"), "+OK")
			expect(t, w.cmd("PASS secret"), "+OK Authentication successful")
			expect(t, w.cmd("STAT"), "+OK 1 42")
			expect(t, w.cmd("NOOP"), "+OK")
		})
	}
}

// checklist: POP3-RESP-POLICY-03
func TestResponseCodeMaildropFailure(t *testing.T) {
	w := connect(t, backend{password: "secret", loadErr: errors.New("private backend detail")})
	for range 2 {
		expect(t, w.cmd("USER alice"), "+OK")
		// Valid credentials followed by Messages failure are not an AUTH error,
		// and the backend's error text never reaches the client.
		expect(t, w.cmd("PASS secret"), "-ERR [SYS/TEMP] Failed to load mailbox")
		expect(t, w.cmd("PASS secret"), "-ERR USER first")
		expect(t, w.cmd("STAT"), "-ERR Not authenticated")
	}
	expect(t, w.cmd("QUIT"), "+OK POP3 server signing off")
}

// nilMailboxBackend accepts the credentials but returns no Mailbox, a backend
// bug the session must refuse rather than dereference.
type nilMailboxBackend struct{}

func (nilMailboxBackend) Authenticate(string, string) (pop3.Mailbox, error) { return nil, nil }

// RFC 3206 §4: a failure after good credentials is a system error. A maildrop
// that fails to load is likely transient (SYS/TEMP); a backend that hands back
// no Mailbox needs intervention (SYS/PERM). Neither is labelled AUTH, neither
// counts against the limiter, and the session stays in AUTHORIZATION.
// checklist: POP3-RESP-CODES-04
func TestResponseCodeSystemFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend pop3.Backend
		want    string
	}{
		{"load-error", backend{password: "secret", loadErr: errors.New("store locked")}, "-ERR [SYS/TEMP] Failed to load mailbox"},
		{"nil-mailbox", nilMailboxBackend{}, "-ERR [SYS/PERM] Failed to load mailbox"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limiter := &authRespBanLimiter{limit: 1}
			client, server := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				pop3server.NewSession(server, tc.backend, nil, limiter).Handle()
			}()
			t.Cleanup(func() { _ = client.Close() })
			w := &wire{t: t, conn: client, reader: bufio.NewReader(client), done: done}
			w.status(w.line(), "+OK")
			for range 2 {
				expect(t, w.cmd("USER alice"), "+OK")
				// Exact equality pins the code, its slash-separated hierarchy and
				// the brackets right after the status space.
				expect(t, w.cmd("PASS secret"), tc.want)
				expect(t, w.cmd("STAT"), "-ERR Not authenticated")
			}
			expect(t, w.cmd("QUIT"), "+OK POP3 server signing off")
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("session still open after QUIT")
			}
			// A limiter that bans on the first recorded failure saw none.
			if limiter.fails != 0 {
				t.Fatalf("RecordAuthFail calls = %d, want 0", limiter.fails)
			}
		})
	}
}

// authRespBanLimiter bans an address once a set number of credential failures
// has been recorded for it.
type authRespBanLimiter struct {
	pop3.NopLimiter
	limit, fails int
}

func (l *authRespBanLimiter) RecordAuthFail(string) { l.fails++ }
func (l *authRespBanLimiter) IsBanned(string) bool  { return l.fails >= l.limit }

// RFC 3206 §6: a server that labels every credential-caused authentication
// failure with AUTH SHOULD list AUTH-RESP-CODE in CAPA. The cases cover each
// credential failure this library reports: a wrong password, an unknown
// account, and the failure that earns a limiter ban and closes the session.
// checklist: POP3-RESP-CODES-03 POP3-RESP-CODES-10
func TestResponseCodeAuthRespCodeCapability(t *testing.T) {
	w := connect(t, backend{password: "secret"})
	for _, authenticated := range []bool{false, true} {
		if authenticated {
			w.login("secret")
		}
		w.status(w.cmd("CAPA"), "+OK")
		count := 0
		for i := 0; ; i++ {
			if i > 32 {
				t.Fatal("CAPA missing terminator")
			}
			line := w.line()
			if line == "." {
				break
			}
			tag, _, _ := strings.Cut(line, " ")
			if strings.EqualFold(tag, "AUTH-RESP-CODE") {
				// The capability takes no parameters.
				expect(t, line, "AUTH-RESP-CODE")
				count++
			}
		}
		if count != 1 {
			t.Fatalf("CAPA authenticated=%t: AUTH-RESP-CODE count = %d, want 1", authenticated, count)
		}
	}

	for _, credentials := range [][2]string{{"alice", "wrong"}, {"unknown", "secret"}} {
		t.Run("ban/"+credentials[0], func(t *testing.T) {
			client, server := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				pop3server.NewSession(server, backend{password: "secret"}, nil, &authRespBanLimiter{limit: 2}).Handle()
			}()
			t.Cleanup(func() { _ = client.Close() })
			w := &wire{t: t, conn: client, reader: bufio.NewReader(client), done: done}
			w.status(w.line(), "+OK")
			expect(t, w.cmd("USER "+credentials[0]), "+OK")
			expect(t, w.cmd("PASS "+credentials[1]), "-ERR [AUTH] Authentication failed")
			expect(t, w.cmd("USER "+credentials[0]), "+OK")
			// The ban follows a credential failure, so it carries AUTH too.
			expect(t, w.cmd("PASS "+credentials[1]), "-ERR [AUTH] Too many authentication failures")
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("session still open after the ban")
			}
		})
	}
}
