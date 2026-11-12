package imap

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// dialRights logs in to a session over an API whose account holds INBOX,
// Archive and "music/rock" under a "music" that is not a folder, counting the
// imports the API receives.
func dialRights(t *testing.T) (*imapClient, *atomic.Int32) {
	t.Helper()
	var imports atomic.Int32
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("/api/v1/auth/protocol-sign-in", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":{"access_token":"tok","user":{"id":1,"email":"dana@example.test"}}}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[{"name":"INBOX"},{"name":"Archive"},{"name":"music/rock"}]}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[]}`)
	})
	mux.HandleFunc("POST /api/v1/accounts/1/messages/import", func(w http.ResponseWriter, r *http.Request) {
		imports.Add(1)
		reply(w, `{"data":{"id":100}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, server := net.Pipe()
	sess := imapserver.NewSession(server, NewBackend(apiclient.New(srv.URL), nil), "imap.test", nil, goimap.NopLimiter{})
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
	c.do("a1", "LOGIN dana@example.test pw")
	return c, &imports
}

// tagged sends lines, reading a continuation after each but the last, and
// returns the command's tagged completion, whatever its status.
func (c *imapClient) tagged(tag string, lines ...string) string {
	c.t.Helper()
	for i, line := range lines {
		if _, err := io.WriteString(c.conn, line+"\r\n"); err != nil {
			c.t.Fatalf("write: %v", err)
		}
		if i < len(lines)-1 {
			if cont := c.read(); !strings.HasPrefix(cont, "+ ") {
				c.t.Fatalf("continuation: %q", cont)
			}
		}
	}
	for {
		if l := c.read(); strings.HasPrefix(l, tag+" ") {
			return strings.TrimSuffix(l, "\r\n")
		}
	}
}

// REST Mail keeps no rights per folder, so the gateway reports every folder of
// the signed-in account readable and writable: SELECT is read-write and APPEND
// discloses APPENDUID. A folder that does not exist is never created by
// APPEND, COPY or MOVE: the client is told TRYCREATE, or a plain NO for a name
// no folder can have, and the API receives nothing. DELETE of "music", which
// is not a folder but has one below it, answers HASCHILDREN (#1671).
func TestMailboxRightsThroughTheSession(t *testing.T) {
	c, imports := dialRights(t)
	if out := c.do("a2", "SELECT INBOX"); !strings.Contains(out, "a2 OK [READ-WRITE] SELECT completed") {
		t.Fatalf("SELECT INBOX: %q", out)
	}
	msg := "Subject: hi\r\n\r\nhi\r\n"
	appendTo := func(tag, folder string) string {
		return c.tagged(tag, fmt.Sprintf("%s APPEND %s {%d}", tag, folder, len(msg)), msg)
	}
	if got := appendTo("a3", "Missing"); got != "a3 NO [TRYCREATE] APPEND failed" {
		t.Fatalf("APPEND to a missing folder: %q", got)
	}
	if got := appendTo("a4", `"Bad/../Name"`); got != "a4 NO [NONEXISTENT] APPEND failed" {
		t.Fatalf("APPEND to a name no folder can have: %q", got)
	}
	if n := imports.Load(); n != 0 {
		t.Fatalf("API received %d imports for refused APPENDs", n)
	}
	if got := appendTo("a5", "Archive"); got != "a5 OK [APPENDUID 1 100] APPEND completed" {
		t.Fatalf("APPEND to Archive: %q", got)
	}
	if got := c.tagged("a6", "a6 DELETE music"); got != "a6 NO [HASCHILDREN] Mailbox has inferior mailboxes" {
		t.Fatalf("DELETE music: %q", got)
	}
}
