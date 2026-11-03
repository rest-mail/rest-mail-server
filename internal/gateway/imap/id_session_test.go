package imap

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"
)

// keepMailbox is the gateway Backend, keeping the mailbox it hands the session
// so a test can read the per-connection state.
type keepMailbox struct {
	*Backend
	got chan *mailbox
}

func (k keepMailbox) Authenticate(user, pass string) (goimap.Mailbox, error) {
	m, err := k.Backend.Authenticate(user, pass)
	if err == nil {
		k.got <- m.(*mailbox)
	}
	return m, err
}

func dialID(t *testing.T) (*imapClient, chan *mailbox) {
	t.Helper()
	client, server := net.Pipe()
	got := make(chan *mailbox, 1)
	sess := imapserver.NewSession(server, keepMailbox{NewBackend(utf8API(t), nil), got}, "imap.test", nil, goimap.NopLimiter{})
	sess.SetServerID("REST Mail", "1.2.3")
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &imapClient{t: t, conn: client, r: bufio.NewReader(client)}
	c.read() // greeting
	return c, got
}

// The gateway advertises ID (RFC 2971) and keeps what imapsync says it is on
// the connection's mailbox, so an import summary (#1498) can name the tool; a
// client that sends no ID, or ID NIL, is served as before.
func TestIDThroughTheSession(t *testing.T) {
	c, got := dialID(t)
	if caps := c.do("a0", "CAPABILITY"); !strings.Contains(caps, " ID") {
		t.Fatalf("CAPABILITY before login lacks ID: %q", caps)
	}
	c.do("a1", "LOGIN jorg@example.test pw")
	if caps := c.do("a2", "CAPABILITY"); !strings.Contains(caps, " ID") || strings.Contains(caps, "IMAP4rev2") {
		t.Fatalf("CAPABILITY after login: %q", caps)
	}
	reply := c.do("a3", `ID ("name" "imapsync" "version" "2.290" "os" "linux" "vendor" "Gilles LAMIRAL" "support-url" "https://imapsync.lamiral.info/" "date" "22-Sep-2025 00:00:00 +0000")`)
	if !strings.HasPrefix(reply, `* ID ("name" "REST Mail" "version" "1.2.3")`+"\r\n") {
		t.Fatalf("ID reply: %q", reply)
	}
	m := <-got
	id := m.ClientID()
	if name, _ := id.Get("name"); name != "imapsync" {
		t.Fatalf("connection holds name %q, want imapsync (%v)", name, id)
	}
	if version, _ := id.Get("version"); version != "2.290" || len(id) != 6 {
		t.Fatalf("connection holds %v", id)
	}
	c.do("a4", "SELECT INBOX")

	silent, got := dialID(t)
	silent.do("b1", "LOGIN jorg@example.test pw")
	silent.do("b2", "ID NIL")
	silent.do("b3", "SELECT INBOX")
	silent.do("b4", "FETCH 1 FLAGS")
	if id := (<-got).ClientID(); id != nil {
		t.Fatalf("a client that sent no ID list left %v", id)
	}
}
