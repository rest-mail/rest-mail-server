package imap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// STATUS DELETED is "the number of messages that have the \Deleted flag set"
// (RFC 9051 §6.3.11). In REST Mail an IMAP \Deleted message is still stored,
// marked imap_deleted, and the folder listing leaves it out unless asked with
// include_imap_deleted. The fake API below does the same, so the count is
// right only when the gateway asks for those messages, and a STORE of
// \Deleted must be written back to show in the next STATUS.
func TestStatusDeletedThroughTheSession(t *testing.T) {
	var mu sync.Mutex
	deleted := map[uint]bool{2: true, 3: false, 4: true}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("/api/v1/gateway-sessions", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":{"token":"rmgs_tok","user":{"id":1,"email":"dana@example.test"}}}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[{"name":"INBOX"}]}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		withDeleted := r.URL.Query().Get("include_imap_deleted") == "true"
		var items []string
		for _, id := range []uint{2, 3, 4} {
			if deleted[id] && !withDeleted {
				continue
			}
			items = append(items, fmt.Sprintf(`{"id":%d,"is_read":true,"imap_deleted":%t,"raw_size":10,"imap_size":10}`, id, deleted[id]))
		}
		reply(w, `{"data":[`+strings.Join(items, ",")+`]}`)
	})
	mux.HandleFunc("/api/v1/messages/3", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if v, ok := body["imap_deleted"].(bool); ok {
			mu.Lock()
			deleted[3] = v
			mu.Unlock()
		}
		reply(w, `{"data":{"id":3}}`)
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
	if err := client.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &imapClient{t: t, conn: client, r: bufio.NewReader(client)}
	c.read() // greeting

	c.do("a1", "LOGIN dana@example.test pw")
	if got := c.do("a2", "STATUS INBOX (MESSAGES DELETED)"); !strings.Contains(got, `* STATUS "INBOX" (MESSAGES 3 DELETED 2)`+"\r\n") {
		t.Fatalf("STATUS before STORE:\n%s", got)
	}
	c.do("a3", "SELECT INBOX")
	c.do("a4", `STORE 2 +FLAGS.SILENT (\Deleted)`)
	if got := c.do("a5", "STATUS INBOX (DELETED)"); !strings.Contains(got, `* STATUS "INBOX" (DELETED 3)`+"\r\n") {
		t.Fatalf("STATUS after STORE \\Deleted:\n%s", got)
	}
	if got := c.do("a6", `LIST "" "*" RETURN (STATUS (DELETED))`); !strings.Contains(got, `* STATUS "INBOX" (DELETED 3)`+"\r\n") {
		t.Fatalf("LIST-STATUS DELETED:\n%s", got)
	}
}
