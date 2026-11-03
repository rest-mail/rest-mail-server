package rfctest

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/server"
)

const (
	testUser = "alice@example.com"
	testPass = "s3cret"
)

// backend accepts one user and counts authentication attempts.
type backend struct {
	calls int
	box   imap.Mailbox
}

func (b *backend) Authenticate(user, pass string) (imap.Mailbox, error) {
	b.calls++
	if user != testUser || pass != testPass {
		return nil, errors.New("invalid credentials")
	}
	if b.box != nil {
		return b.box, nil
	}
	return mailbox{}, nil
}

// mailbox holds one INBOX message.
type mailbox struct{}

func (mailbox) Folders() ([]imap.Folder, error) { return []imap.Folder{{Name: "INBOX"}}, nil }

func (mailbox) Messages(folder string) ([]imap.Message, error) {
	if folder != "INBOX" {
		return nil, nil
	}
	return []imap.Message{{UID: 1, Size: 24, Subject: "hello"}}, nil
}

func (mailbox) Fetch(uint32) ([]byte, error) { return []byte("Subject: hello\r\n\r\nhi!\r\n"), nil }

func (mailbox) Store(uint32, imap.FlagUpdate) error          { return nil }
func (mailbox) Move(uint32, string) error                    { return nil }
func (mailbox) Delete(uint32) error                          { return nil }
func (mailbox) Copy(uint32, string) error                    { return nil }
func (mailbox) Append(string, imap.FlagUpdate, []byte) error { return nil }
func (mailbox) Quota() (used, limit int64, err error)        { return 0, 0, nil }

// client is the test's end of one session.
type client struct {
	t       *testing.T
	conn    net.Conn
	r       *bufio.Reader
	backend *backend
	done    chan struct{}
}

// dial starts a session on a pipe and returns the client end, the greeting
// still unread.
func dial(t *testing.T) *client {
	t.Helper()
	return dialMailbox(t, nil)
}

// dialMailbox supplies an account fixture to the same public-session harness.
func dialMailbox(t *testing.T, box imap.Mailbox) *client {
	t.Helper()
	return dialConn(t, box, nil)
}

// dialConn optionally wraps the session's transport to inject read failures.
func dialConn(t *testing.T, box imap.Mailbox, wrap func(net.Conn) net.Conn) *client {
	t.Helper()
	return dialTLS(t, box, wrap, nil)
}

// dialTLS starts the session with a TLS config, enabling STARTTLS.
func dialTLS(t *testing.T, box imap.Mailbox, wrap func(net.Conn) net.Conn, cfg *tls.Config) *client {
	t.Helper()
	return dialConfigured(t, box, wrap, cfg, nil)
}

// dialConfigured starts the session after configure has set it up, such as its IDLE poll
// interval.
func dialConfigured(t *testing.T, box imap.Mailbox, wrap func(net.Conn) net.Conn, cfg *tls.Config, configure func(*server.Session)) *client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	if wrap != nil {
		serverConn = wrap(serverConn)
	}
	b := &backend{box: box}
	sess := server.NewSession(serverConn, b, "imap.test", cfg, imap.NopLimiter{})
	if configure != nil {
		configure(sess)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	if err := clientConn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, conn: clientConn, r: bufio.NewReader(clientConn), backend: b, done: done}
	t.Cleanup(func() {
		_ = clientConn.Close()
		<-done
	})
	return c
}

// line reads one response line without its CRLF.
func (c *client) line() string {
	c.t.Helper()
	l, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	if !strings.HasSuffix(l, "\r\n") {
		c.t.Fatalf("response line %q does not end in CRLF", l)
	}
	return strings.TrimSuffix(l, "\r\n")
}

// command sends one command line and reads up to the response tagged tag. The
// test commands draw no literals, so lines are read one by one.
func (c *client) command(tag, format string, args ...any) (untagged []string, tagged string) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, tag+" "+format+"\r\n", args...); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	for {
		l := c.line()
		if strings.HasPrefix(l, tag+" ") {
			return untagged, l
		}
		if !strings.HasPrefix(l, "* ") {
			c.t.Fatalf("%s: line %q is neither untagged nor tagged %s", tag, l, tag)
		}
		untagged = append(untagged, l)
	}
}

// login authenticates as the test user and fails the test otherwise.
func (c *client) login() {
	c.t.Helper()
	if _, tagged := c.command("L1", "LOGIN %s %s", testUser, testPass); !strings.HasPrefix(tagged, "L1 OK") {
		c.t.Fatalf("LOGIN: %q", tagged)
	}
}

// closed reports whether the server has closed its end: the next read sees EOF.
func (c *client) closed() bool {
	c.t.Helper()
	_, err := c.r.ReadByte()
	return errors.Is(err, io.EOF)
}
