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

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// A message is \Recent to the first read-write IMAP session that selects its
// folder on this gateway, and to no later one; EXAMINE and STATUS report it
// without claiming it (RFC 3501 §2.3.2, #1860). Another account keeps its own
// state, and a gateway that starts afresh cannot know who was first, so it
// counts the messages recent again.
func TestRecentSharedByTheGatewaysSessions(t *testing.T) {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("/api/v1/auth/protocol-sign-in", func(w http.ResponseWriter, r *http.Request) {
		user := "1"
		if email, _, _ := r.BasicAuth(); email == "erin@example.test" {
			user = "2"
		}
		reply(w, `{"data":{"access_token":"tok","user":{"id":`+user+`}}}`)
	})
	for _, account := range []string{"1", "2"} {
		mux.HandleFunc("/api/v1/accounts/"+account+"/folders", func(w http.ResponseWriter, r *http.Request) {
			reply(w, `{"data":[{"name":"INBOX","total":2,"unread":1,"uid_next":8}]}`)
		})
		mux.HandleFunc("/api/v1/accounts/"+account+"/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
			reply(w, `{"data":[{"id":5,"is_read":true,"raw_size":10},{"id":7,"raw_size":10}]}`)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	open := func(backend *Backend, user string) *imapClient {
		client, server := net.Pipe()
		sess := imapserver.NewSession(server, backend, "imap.test", nil, goimap.NopLimiter{})
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
		if got := c.do("a1", "LOGIN "+user+" pw"); !strings.Contains(got, "a1 OK") {
			t.Fatalf("LOGIN: %s", got)
		}
		return c
	}
	expect := func(got string, want ...string) {
		t.Helper()
		for _, line := range want {
			if !strings.Contains(got, line+"\r\n") {
				t.Fatalf("missing %q in:\n%s", line, got)
			}
		}
	}

	gateway := NewBackend(apiclient.New(srv.URL), nil)
	reader := open(gateway, "dana@example.test")
	expect(reader.do("b1", "EXAMINE INBOX"), "* 2 RECENT")
	expect(reader.do("b2", "STATUS INBOX (RECENT)"), `* STATUS "INBOX" (RECENT 2)`)

	first := open(gateway, "dana@example.test")
	expect(first.do("c1", "SELECT inbox"), "* 2 RECENT")
	expect(first.do("c2", "FETCH 1:2 (FLAGS)"), `* 1 FETCH (UID 5 FLAGS (\Seen \Recent))`, `* 2 FETCH (UID 7 FLAGS (\Recent))`)
	expect(first.do("c3", "SEARCH NEW"), "* SEARCH 2")

	later := open(gateway, "dana@example.test")
	expect(later.do("d1", "SELECT INBOX"), "* 0 RECENT")
	expect(later.do("d2", "FETCH 1:2 (FLAGS)"), `* 1 FETCH (UID 5 FLAGS (\Seen))`, `* 2 FETCH (UID 7 FLAGS ())`)
	expect(later.do("d3", "SEARCH OLD"), "* SEARCH 1 2")
	expect(later.do("d4", "STATUS INBOX (RECENT)"), `* STATUS "INBOX" (RECENT 0)`)

	other := open(gateway, "erin@example.test")
	expect(other.do("e1", "SELECT INBOX"), "* 2 RECENT")

	restarted := open(NewBackend(apiclient.New(srv.URL), nil), "dana@example.test")
	expect(restarted.do("f1", "SELECT INBOX"), "* 2 RECENT")
}
