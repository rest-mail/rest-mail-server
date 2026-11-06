package imap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// importSessionAPI is the fake API with import sessions (#1498): it records the
// sessions opened and closed and the session each import names.
type importSessionAPI struct {
	*fakeAPI
	mu      sync.Mutex
	opened  []apitypes.OpenImportSessionBody
	closed  []string
	tagged  map[string]int
	expired map[string]bool
}

func (a *importSessionAPI) handler() http.Handler {
	inner := a.fakeAPI.handler()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/accounts/{acct}/messages/import-sessions", func(w http.ResponseWriter, r *http.Request) {
		var body apitypes.OpenImportSessionBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		a.opened = append(a.opened, body)
		id := fmt.Sprintf("%032x", len(a.opened))
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(endpoint.Data[apitypes.ImportSession]{Data: apitypes.ImportSession{ID: id}})
	})
	mux.HandleFunc("DELETE /api/v1/accounts/{acct}/messages/import-sessions/{session}", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.closed = append(a.closed, r.PathValue("session"))
		a.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/v1/accounts/{acct}/messages/import", func(w http.ResponseWriter, r *http.Request) {
		session := r.URL.Query().Get("import_session")
		a.mu.Lock()
		gone := a.expired[session]
		if !gone {
			a.tagged[session]++
		}
		a.mu.Unlock()
		if gone {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"import_session_not_found","message":"gone"}}`))
			return
		}
		inner.ServeHTTP(w, r)
	})
	mux.Handle("/", inner)
	return mux
}

func newImportSessionAPI() *importSessionAPI {
	return &importSessionAPI{fakeAPI: newFakeAPI(), tagged: map[string]int{}, expired: map[string]bool{}}
}

// One IMAP connection that APPENDs is one import: the gateway opens one session at
// the first APPEND, naming the client from imapsync's ID, sends it with every
// APPEND, and closes it once at LOGOUT, which writes the one summary.
func TestServer_ImportSessionPerConnection(t *testing.T) {
	api := newImportSessionAPI()
	server := httptest.NewServer(api.handler())
	t.Cleanup(server.Close)
	c := newTranscript(t, NewBackend(apiclient.New(server.URL), nil))
	c.login("a1")
	// imapsync's ID form (sub imap_id): name, version, os, vendor, support-url, date.
	if _, status := c.command("a2", `ID ("name" "imapsync" "version" "2.290" "os" "linux" "vendor" "Gilles LAMIRAL" "support-url" "https://imapsync.lamiral.info/" "date" "22-Dec-2024 13:00:00 +0000")`); !strings.HasPrefix(status, "a2 OK") {
		t.Fatalf("ID: %q", status)
	}
	const appends = 1000
	for i := range appends {
		tag := fmt.Sprintf("p%d", i)
		folder := "Archive"
		if i%2 == 1 {
			folder = "Sent"
		}
		if _, status := c.appendMsg(tag, folder, fmt.Sprintf("Message-ID: <m%d@example.test>\r\nSubject: %d\r\n\r\nbody\r\n", i, i)); !strings.HasPrefix(status, tag+" OK") {
			t.Fatalf("APPEND %d: %q", i, status)
		}
	}
	api.mu.Lock()
	if len(api.closed) != 0 {
		t.Fatalf("closed before LOGOUT: %q", api.closed)
	}
	api.mu.Unlock()
	if _, status := c.command("a3", "LOGOUT"); !strings.HasPrefix(status, "a3 OK") {
		t.Fatalf("LOGOUT: %q", status)
	}
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end after LOGOUT")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.opened) != 1 || api.opened[0].ClientName != "imapsync" || api.opened[0].ClientVersion != "2.290" {
		t.Fatalf("opened %+v, want one session for imapsync 2.290", api.opened)
	}
	session := fmt.Sprintf("%032x", 1)
	if len(api.tagged) != 1 || api.tagged[session] != appends {
		t.Fatalf("imports by session %v, want all %d in %s", api.tagged, appends, session)
	}
	if len(api.closed) != 1 || api.closed[0] != session {
		t.Fatalf("closed %q, want %s once", api.closed, session)
	}
}

// A connection that drops without LOGOUT still closes its session; one that never
// imports opens none; a session the API swept as idle is replaced, not lost.
func TestServer_ImportSessionDisconnectAndExpiry(t *testing.T) {
	api := newImportSessionAPI()
	server := httptest.NewServer(api.handler())
	t.Cleanup(server.Close)
	backend := NewBackend(apiclient.New(server.URL), nil)

	idle := newTranscript(t, backend)
	idle.login("i1")
	if _, status := idle.command("i2", "LOGOUT"); !strings.HasPrefix(status, "i2 OK") {
		t.Fatalf("LOGOUT: %q", status)
	}
	<-idle.done

	c := newTranscript(t, backend)
	c.login("a1")
	if _, status := c.appendMsg("a2", "Archive", "Message-ID: <x1@example.test>\r\n\r\nbody\r\n"); !strings.HasPrefix(status, "a2 OK") {
		t.Fatalf("APPEND: %q", status)
	}
	first := fmt.Sprintf("%032x", 1)
	api.mu.Lock()
	api.expired[first] = true
	api.mu.Unlock()
	if _, status := c.appendMsg("a3", "Archive", "Message-ID: <x2@example.test>\r\n\r\nbody\r\n"); !strings.HasPrefix(status, "a3 OK") {
		t.Fatalf("APPEND after expiry: %q", status)
	}
	_ = c.conn.Close() // vanish without LOGOUT
	<-c.done
	api.mu.Lock()
	defer api.mu.Unlock()
	second := fmt.Sprintf("%032x", 2)
	if len(api.opened) != 2 || api.tagged[first] != 1 || api.tagged[second] != 1 {
		t.Fatalf("opened %d, tagged %v; want the swept session replaced", len(api.opened), api.tagged)
	}
	if len(api.closed) != 1 || api.closed[0] != second {
		t.Fatalf("closed %q on disconnect, want %s", api.closed, second)
	}
}
