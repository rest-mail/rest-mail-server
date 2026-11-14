package imap

import (
	"bufio"
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

// UIDNEXT must not change unless messages are added, and must change whenever
// they are, even if they are expunged later (RFC 3501 §2.3.1.1, #804). The fake
// API below keeps INBOX the way REST Mail does: IDs come from one ascending
// sequence, an expunge removes the row for good, and the folder listing's
// uid_next is one above the highest ID the folder has ever held.
func TestUIDNextSurvivesExpungeThroughTheSession(t *testing.T) {
	var mu sync.Mutex
	inbox := []uint{101, 105}
	var nextID, highest uint = 106, 105
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux.HandleFunc("/api/v1/gateway-sessions", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":{"token":"rmgs_tok","user":{"id":1,"email":"dana@example.test"}}}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reply(w, fmt.Sprintf(`{"data":[{"name":"INBOX","total":%d,"unread":0,"uid_next":%d},{"name":"Sent","total":0,"unread":0,"uid_next":1}]}`, len(inbox), highest+1))
	})
	mux.HandleFunc("/api/v1/accounts/1/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var items []string
		for _, id := range inbox {
			items = append(items, fmt.Sprintf(`{"id":%d,"is_read":true,"imap_deleted":%t,"raw_size":10}`, id, id == 105))
		}
		reply(w, `{"data":[`+strings.Join(items, ",")+`]}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders/Sent/messages", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[]}`)
	})
	mux.HandleFunc("/api/v1/messages/105", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "unexpected", http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		inbox = []uint{101}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/v1/accounts/1/messages/import", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		id := nextID
		nextID++
		inbox = append(inbox, id)
		highest = id
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"data":{"id":%d,"folder":"INBOX","imported":true}}`, id)
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
	if got := c.do("a2", "SELECT INBOX"); !strings.Contains(got, "* OK [UIDNEXT 106] ") {
		t.Fatalf("SELECT before EXPUNGE:\n%s", got)
	}
	// 105, the newest, carries \Deleted in the store.
	c.do("a3", "EXPUNGE")
	for tag, cmd := range map[string]string{"a4": "SELECT INBOX", "a5": "EXAMINE inbox"} {
		if got := c.do(tag, cmd); !strings.Contains(got, "* OK [UIDNEXT 106] ") {
			t.Fatalf("%s after expunging UID 105:\n%s", cmd, got)
		}
	}
	if got := c.do("a6", "STATUS INBOX (MESSAGES UIDNEXT)"); !strings.Contains(got, `* STATUS "INBOX" (MESSAGES 1 UIDNEXT 106)`+"\r\n") {
		t.Fatalf("STATUS after expunging UID 105:\n%s", got)
	}

	got := c.do("a7", "APPEND INBOX {2+}\r\nhi")
	if !strings.Contains(got, "[APPENDUID 1 106]") {
		t.Fatalf("APPEND after the expunge:\n%s", got)
	}
	if got := c.do("a8", "STATUS INBOX (UIDNEXT)"); !strings.Contains(got, `* STATUS "INBOX" (UIDNEXT 107)`+"\r\n") {
		t.Fatalf("STATUS after APPEND:\n%s", got)
	}
	// A folder that never held a message starts at 1.
	if got := c.do("a9", "STATUS Sent (UIDNEXT)"); !strings.Contains(got, `* STATUS "Sent" (UIDNEXT 1)`+"\r\n") {
		t.Fatalf("STATUS Sent:\n%s", got)
	}
}
