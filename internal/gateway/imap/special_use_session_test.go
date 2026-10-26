package imap

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// A mail app learns which folder is which from LIST (RFC 6154): REST Mail's
// standard folders carry their roles, Spam as \Junk and Archive as \Archive,
// so the app does not make a Junk or Archive of its own beside them (#717).
func TestSpecialUseThroughTheSession(t *testing.T) {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("/api/v1/auth/protocol-sign-in", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":{"access_token":"tok","user":{"id":1,"email":"dana@example.test"}}}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[{"name":"INBOX"},{"name":"Sent"},{"name":"Drafts"},{"name":"Trash"},{"name":"Spam"},{"name":"Archive"},{"name":"Projects"}]}`)
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
	if caps := c.do("a2", "CAPABILITY"); !strings.Contains(caps, " SPECIAL-USE") {
		t.Fatalf("CAPABILITY lacks SPECIAL-USE: %q", caps)
	}
	all := c.do("a3", `LIST "" "*"`)
	for _, want := range []string{
		`* LIST () "/" "INBOX"`,
		`* LIST (\Sent) "/" "Sent"`,
		`* LIST (\Drafts) "/" "Drafts"`,
		`* LIST (\Trash) "/" "Trash"`,
		`* LIST (\Junk) "/" "Spam"`,
		`* LIST (\Archive) "/" "Archive"`,
		`* LIST () "/" "Projects"`,
	} {
		if !strings.Contains(all, want+"\r\n") {
			t.Errorf("LIST lacks %s:\n%s", want, all)
		}
	}
	roles := c.do("a4", `LIST (SPECIAL-USE) "" "*"`)
	if strings.Contains(roles, `"INBOX"`) || strings.Contains(roles, `"Projects"`) || strings.Count(roles, "* LIST ") != 5 {
		t.Errorf("LIST (SPECIAL-USE) should list exactly the five role folders:\n%s", roles)
	}
}
