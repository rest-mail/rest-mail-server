package imap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// \Deleted is a permanent flag (RFC 3501 §2.3.2). The gateway keeps it as the
// message's imap_deleted state in the API, so every session and device sees
// one mark, and EXPUNGE removes what the API marks when it runs. The fake API
// below keeps imap_deleted per message, lists marked messages only with
// include_imap_deleted, and records each permanent delete.
func TestDeletedMarkSharedThroughTheAPI(t *testing.T) {
	var mu sync.Mutex
	ids := []uint{2, 3, 4}
	deleted := map[uint]bool{4: true}
	var removed []uint
	reply := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/protocol-sign-in", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":{"access_token":"tok","user":{"id":1,"email":"dana@example.test"}}}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"data":[{"name":"INBOX"}]}`)
	})
	mux.HandleFunc("/api/v1/accounts/1/folders/INBOX/messages", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		withDeleted := r.URL.Query().Get("include_imap_deleted") == "true"
		var items []string
		for _, id := range ids {
			if deleted[id] && !withDeleted {
				continue
			}
			items = append(items, fmt.Sprintf(`{"id":%d,"imap_deleted":%t,"raw_size":10}`, id, deleted[id]))
		}
		reply(w, `{"data":[`+strings.Join(items, ",")+`]}`)
	})
	mux.HandleFunc("/api/v1/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		var id uint
		if _, err := fmt.Sscan(r.PathValue("id"), &id); err != nil {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodDelete:
			if r.URL.Query().Get("permanent") != "true" {
				http.Error(w, "not permanent", http.StatusBadRequest)
				return
			}
			removed = append(removed, id)
			ids = slices.DeleteFunc(ids, func(v uint) bool { return v == id })
			w.WriteHeader(http.StatusNoContent)
		default:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if v, ok := body["imap_deleted"].(bool); ok {
				deleted[id] = v
			}
			reply(w, fmt.Sprintf(`{"data":{"id":%d}}`, id))
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	open := func() *imapClient {
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
		c.do("a2", "SELECT INBOX")
		return c
	}

	desktop := open()
	phone := open()
	if got := phone.do("b1", "FETCH 3 (UID FLAGS)"); !strings.Contains(got, `* 3 FETCH (UID 4 FLAGS (\Deleted))`) {
		t.Fatalf("a mark already in the API is missing:\n%s", got)
	}

	// The desktop marks UID 3; the phone clears that mark and marks UID 2.
	desktop.do("c1", `STORE 2 +FLAGS.SILENT (\Deleted)`)
	phone.do("c2", `STORE 2 -FLAGS.SILENT (\Deleted)`)
	phone.do("c3", `STORE 1 +FLAGS.SILENT (\Deleted)`)
	if got := phone.do("c4", "FETCH 1:2 (UID FLAGS)"); !strings.Contains(got, `* 1 FETCH (UID 2 FLAGS (\Deleted))`) || strings.Contains(got, `UID 3 FLAGS (\Deleted)`) {
		t.Fatalf("phone flags after its STOREs:\n%s", got)
	}

	// The desktop never polled, yet EXPUNGE removes what the API marks now:
	// UID 2 and UID 4, not UID 3, whose mark the phone cleared.
	got := desktop.do("d1", "UID EXPUNGE 2:3")
	if !strings.Contains(got, "* 1 EXPUNGE\r\n") || strings.Count(got, "EXPUNGE\r\n") != 1 {
		t.Fatalf("UID EXPUNGE 2:3:\n%s", got)
	}
	got = desktop.do("d2", "EXPUNGE")
	if !strings.Contains(got, "* 2 EXPUNGE\r\n") || strings.Count(got, "EXPUNGE\r\n") != 1 {
		t.Fatalf("EXPUNGE:\n%s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(removed, []uint{2, 4}) || !slices.Equal(ids, []uint{3}) {
		t.Fatalf("API removed %v and kept %v, want removed [2 4] and kept [3]", removed, ids)
	}
}
