// Package rfctest holds the explicitly partial RFC checklist's wire proofs.
package rfctest

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	pop3 "github.com/rest-mail/go-pop3/pkg/pop3"
)

// These fixtures use only memory and net.Pipe; they never open a database or
// port. The implicit-TLS proofs are the one exception: see implicit_tls_test.go.
type backend struct {
	password string
	loadErr  error
}

func (b backend) Authenticate(user, pass string) (pop3.Mailbox, error) {
	if user != "alice" || pass != b.password {
		return nil, errors.New("invalid credentials")
	}
	return mailbox{loadErr: b.loadErr}, nil
}

type mailbox struct{ loadErr error }

func (m mailbox) Messages() ([]pop3.Message, error) {
	return []pop3.Message{{UID: "one", Size: 42}}, m.loadErr
}
func (mailbox) Retrieve(string) ([]byte, error) { return nil, errors.New("unused") }
func (mailbox) MarkSeen(string) error           { return nil }
func (mailbox) Delete(string) error             { return errors.New("unused") }

type wire struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
	done   <-chan struct{}
}

func connect(t *testing.T, b pop3.Backend) *wire {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); pop3server.NewSession(server, b, nil, nil).Handle() }()
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("session did not stop")
		}
	})
	w := &wire{t: t, conn: client, reader: bufio.NewReader(client), done: done}
	w.status(w.line(), "+OK")
	return w
}
func (w *wire) line() string {
	w.t.Helper()
	_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := w.reader.ReadString('\n')
	if err != nil {
		w.t.Fatalf("read response: %v (partial %q)", err, line)
	}
	if !strings.HasSuffix(line, "\r\n") {
		w.t.Fatalf("response lacks CRLF: %q", line)
	}
	if len(line) > 512 {
		w.t.Fatalf("response exceeds 512 octets: %d", len(line))
	}
	return strings.TrimSuffix(line, "\r\n")
}
func (w *wire) cmd(command string) string {
	w.t.Helper()
	_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(w.conn, command+"\r\n"); err != nil {
		w.t.Fatalf("send command: %v", err)
	}
	return w.line()
}
func (w *wire) status(line, want string) {
	w.t.Helper()
	if line != want && !strings.HasPrefix(line, want+" ") {
		w.t.Fatalf("response = %q, want status %s", line, want)
	}
}
func (w *wire) login(password string) {
	w.t.Helper()
	w.status(w.cmd("USER alice"), "+OK")
	w.status(w.cmd("PASS "+password), "+OK")
}

// checklist: POP3-AUTH-01 POP3-AUTH-02 POP3-USER-01 POP3-PASS-01 POP3-PASS-02 POP3-STLS-12
func TestAuthorizationUserPass(t *testing.T) {
	w := connect(t, backend{password: "secret"})
	for _, cmd := range []string{"STAT", "LIST", "UIDL", "RETR 1", "TOP 1 0", "DELE 1", "NOOP", "RSET"} {
		w.status(w.cmd(cmd), "-ERR")
	}
	w.status(w.cmd("USER"), "-ERR")
	w.status(w.cmd("PASS secret"), "-ERR")
	w.status(w.cmd("user alice"), "+OK")
	// USER alone must not grant maildrop access.
	w.status(w.cmd("STAT"), "-ERR")
	w.status(w.cmd("PASS"), "-ERR")
	// Restart USER/PASS so this proof does not claim immediate-PASS sequencing.
	w.status(w.cmd("USER alice"), "+OK")
	w.status(w.cmd("pass secret"), "+OK")
	if got := w.cmd("STAT"); got != "+OK 1 42" {
		t.Fatalf("STAT after login = %q", got)
	}
	w.status(w.cmd("USER alice"), "-ERR")
	w.status(w.cmd("PASS secret"), "-ERR")
}

// checklist: POP3-PASS-03
func TestAuthenticationFailureRetry(t *testing.T) {
	w := connect(t, backend{password: "secret"})
	for _, credentials := range [][2]string{{"alice", "wrong"}, {"unknown", "secret"}} {
		w.status(w.cmd("USER "+credentials[0]), "+OK")
		w.status(w.cmd("PASS "+credentials[1]), "-ERR")
		w.status(w.cmd("STAT"), "-ERR")
	}
	w.login("secret")
	w.status(w.cmd("STAT"), "+OK")
}

// checklist: POP3-AUTH-03
func TestMaildropOpenFailure(t *testing.T) {
	w := connect(t, backend{password: "secret", loadErr: errors.New("maildrop unavailable")})
	for range 2 {
		w.status(w.cmd("USER alice"), "+OK")
		w.status(w.cmd("PASS secret"), "-ERR")
		w.status(w.cmd("STAT"), "-ERR")
	}
}

// checklist: POP3-PASS-04
func TestPasswordContainsSpaces(t *testing.T) {
	w := connect(t, backend{password: "two words"})
	w.login("two words")
	w.status(w.cmd("STAT"), "+OK")
}

// checklist: POP3-AUTH-04
func TestAuthorizationQuit(t *testing.T) {
	for _, user := range []bool{false, true} {
		t.Run(map[bool]string{false: "greeting", true: "after-user"}[user], func(t *testing.T) {
			w := connect(t, backend{password: "secret"})
			if user {
				w.status(w.cmd("USER alice"), "+OK")
			}
			w.status(w.cmd("QUIT"), "+OK")
			_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := w.reader.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("after QUIT: %v, want EOF", err)
			}
		})
	}
}

// checklist: POP3-CAPA-01 POP3-CAPA-02 POP3-CAPA-03 POP3-CAPA-04 POP3-STLS-16
func TestCapaBothStates(t *testing.T) {
	w := connect(t, backend{password: "secret"})
	var before map[string]bool
	for _, authenticated := range []bool{false, true} {
		if authenticated {
			w.login("secret")
		}
		w.status(w.cmd("CAPA unexpected"), "-ERR")
		w.status(w.cmd("capa"), "+OK")
		caps := map[string]bool{}
		for i := 0; ; i++ {
			if i > 32 {
				t.Fatal("CAPA missing terminator")
			}
			line := w.line()
			if line == "." {
				break
			}
			// Current advertised capabilities take no arguments. No claim is made
			// here about the semantics of TOP, UIDL, RESP-CODES or PIPELINING.
			if line == "" {
				t.Fatal("empty capability")
			}
			for _, c := range []byte(line) {
				if c < 0x21 || c > 0x7e || c == '.' {
					t.Fatalf("invalid capability tag %q", line)
				}
			}
			tag := strings.ToUpper(line)
			if caps[tag] {
				t.Fatalf("duplicate capability %q", tag)
			}
			caps[tag] = true
		}
		if !caps["USER"] {
			t.Fatal("CAPA omitted parameterless USER")
		}
		if !authenticated {
			before = caps
		} else {
			for tag := range before {
				if !caps[tag] {
					t.Errorf("CAPA dropped %s after authentication", tag)
				}
			}
		}
		// A fresh status response proves the dot terminator consumed the block.
		if authenticated {
			w.status(w.cmd("STAT"), "+OK")
		} else {
			w.status(w.cmd("STAT"), "-ERR")
		}
	}
}
