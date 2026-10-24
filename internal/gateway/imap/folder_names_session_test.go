package imap

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	imapsrv "github.com/rest-mail/go-imap"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// REST Mail names folders in UTF-8: the webmail, the app and the API make
// "Entwürfe" as it is. A mail app that has not enabled UTF8=ACCEPT reads and
// writes mailbox names in modified UTF-7 (RFC 3501 §5.1.3), so the gateway's
// names go out as "Entw&APw-rfe", and the name the app sends back reaches the
// API as "Entwürfe" again.
func TestFolderNamesInModifiedUTF7ThroughTheSession(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("/api/v1/auth/protocol-sign-in", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":{"access_token":"tok","user":{"id":1,"email":"dana@example.test"}}}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[{"name":"INBOX"},{"name":"Entwürfe"},{"name":"Tom & Jerry"}]}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders/{folder}/messages", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.PathValue("folder"))
		mu.Unlock()
		reply(w, `{"data":[]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, server := net.Pipe()
	sess := imapsrv.NewSession(server, NewBackend(apiclient.New(srv.URL), nil), "imap.test", nil, imapsrv.NopLimiter{})
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
	all := c.do("a2", `LIST "" "*"`)
	for _, want := range []string{`* LIST () "/" "Entw&APw-rfe"`, `* LIST () "/" "Tom &- Jerry"`} {
		if !strings.Contains(all, want+"\r\n") {
			t.Errorf("LIST lacks %s:\n%s", want, all)
		}
	}
	if strings.Contains(all, "ü") {
		t.Errorf("LIST sent 8-bit names to a client that has not enabled UTF-8:\n%s", all)
	}
	c.do("a3", `SELECT "Entw&APw-rfe"`)
	c.do("a4", `STATUS "Tom &- Jerry" (MESSAGES)`)

	mu.Lock()
	defer mu.Unlock()
	if want := []string{"Entwürfe", "Tom & Jerry"}; strings.Join(asked, "|") != strings.Join(want, "|") {
		t.Fatalf("the API was asked for %q, want %q", asked, want)
	}
}
