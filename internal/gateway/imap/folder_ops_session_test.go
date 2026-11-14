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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// folderAPI is a fake API keeping an account's folders apart from their
// messages, as the createFolder, renameFolder and deleteFolder routes do: each
// folder holds message IDs, and INBOX is a standard folder they refuse.
type folderAPI struct {
	mu      sync.Mutex
	folders map[string][]uint
	nextID  uint
	calls   []string
}

func (f *folderAPI) names() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.folders))
	for name := range f.folders {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

// holds is the message IDs in folder.
func (f *folderAPI) holds(folder string) []uint {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.folders[folder])
}

func (f *folderAPI) serve(t *testing.T) *httptest.Server {
	t.Helper()
	reply := func(w http.ResponseWriter, status int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
	nameIn := func(r *http.Request) string {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		return body.Name
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/gateway-sessions", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, `{"data":{"token":"rmgs_tok","user":{"id":1,"email":"dana@example.test"}}}`)
	})
	mux.HandleFunc("GET /api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		var items []string
		for _, name := range strings.Fields(f.names()) {
			items = append(items, fmt.Sprintf(`{"name":%q}`, name))
		}
		reply(w, http.StatusOK, `{"data":[`+strings.Join(items, ",")+`]}`)
	})
	mux.HandleFunc("POST /api/v1/accounts/1/folders", func(w http.ResponseWriter, r *http.Request) {
		name := nameIn(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, "create "+name)
		if _, ok := f.folders[name]; ok {
			reply(w, http.StatusConflict, `{"error":{"code":"already_exists"}}`)
			return
		}
		f.folders[name] = []uint{}
		reply(w, http.StatusCreated, fmt.Sprintf(`{"data":{"name":%q}}`, name))
	})
	mux.HandleFunc("GET /api/v1/accounts/1/folders/{folder}/messages", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var items []string
		for _, id := range f.folders[r.PathValue("folder")] {
			items = append(items, fmt.Sprintf(`{"id":%d,"raw_size":10}`, id))
		}
		reply(w, http.StatusOK, `{"data":[`+strings.Join(items, ",")+`]}`)
	})
	mux.HandleFunc("PATCH /api/v1/accounts/1/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		from, to := r.PathValue("folder"), nameIn(r)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, "rename "+from+" "+to)
		ids, ok := f.folders[from]
		switch {
		case from == "INBOX":
			reply(w, http.StatusUnprocessableEntity, `{"error":{"code":"validation_failed"}}`)
		case !ok:
			reply(w, http.StatusNotFound, `{"error":{"code":"not_found"}}`)
		default:
			if _, taken := f.folders[to]; taken {
				reply(w, http.StatusConflict, `{"error":{"code":"already_exists"}}`)
				return
			}
			delete(f.folders, from)
			f.folders[to] = ids
			reply(w, http.StatusOK, fmt.Sprintf(`{"data":{"name":%q}}`, to))
		}
	})
	mux.HandleFunc("DELETE /api/v1/accounts/1/folders/{folder}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("folder")
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, "delete "+name)
		if name == "INBOX" {
			reply(w, http.StatusUnprocessableEntity, `{"error":{"code":"validation_failed"}}`)
			return
		}
		delete(f.folders, name)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, fmt.Sprintf(`{"data":{"id":%s,"mailbox_id":1,"subject":"hi","body_text":"hi"}}`, r.PathValue("id")))
	})
	mux.HandleFunc("GET /api/v1/messages/{id}/raw", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("POST /api/v1/accounts/1/messages/import", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Folder string `json:"folder"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.nextID++
		f.folders[body.Folder] = append(f.folders[body.Folder], f.nextID)
		reply(w, http.StatusOK, fmt.Sprintf(`{"data":{"id":%d,"imported":true}}`, f.nextID))
	})
	mux.HandleFunc("DELETE /api/v1/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		var id uint
		if _, err := fmt.Sscan(r.PathValue("id"), &id); err != nil {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for name, ids := range f.folders {
			f.folders[name] = slices.DeleteFunc(ids, func(v uint) bool { return v == id })
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// CREATE, DELETE and RENAME go to the API's folder routes (go-imap#160), so a
// folder exists apart from its messages: one created empty stays in the next
// LIST, DELETE removes it with its messages, RENAME keeps its messages and
// renames its inferiors, and RENAME INBOX moves INBOX's messages into a new
// folder, leaving INBOX empty (RFC 9051 §§6.3.4-6.3.6).
func TestFolderOperationsThroughTheAPI(t *testing.T) {
	api := &folderAPI{
		folders: map[string][]uint{"INBOX": {1, 2}, "Trash": {}, "Work": {3}, "Work/Q1": {4}},
		nextID:  10,
	}
	srv := api.serve(t)

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

	if got := c.tagged("b1", "b1 CREATE Projects"); got != "b1 OK CREATE completed" {
		t.Fatalf("CREATE Projects: %q", got)
	}
	if out := c.do("b2", `LIST "" Projects`); !strings.Contains(out, `"/" "Projects"`) {
		t.Fatalf("LIST after CREATE does not show the empty folder:\n%s", out)
	}
	if got := c.tagged("b3", "b3 CREATE Projects"); got != "b3 NO [ALREADYEXISTS] Mailbox already exists" {
		t.Fatalf("CREATE of an extant folder: %q", got)
	}
	if got := c.tagged("b4", `b4 CREATE "Bad/../Name"`); got != "b4 NO [CANNOT] Mailbox cannot be created" {
		t.Fatalf("CREATE of a name the API refuses: %q", got)
	}

	if got := c.tagged("c1", "c1 RENAME Work Play"); got != "c1 OK RENAME completed" {
		t.Fatalf("RENAME Work Play: %q", got)
	}
	if got := api.names(); got != "INBOX Play Play/Q1 Projects Trash" {
		t.Fatalf("folders after RENAME: %q", got)
	}
	if ids := api.holds("Play/Q1"); !slices.Equal(ids, []uint{4}) {
		t.Fatalf("Play/Q1 holds %v, want the inferior's message 4 kept", ids)
	}

	if got := c.tagged("d1", "d1 DELETE Play"); got != "d1 OK DELETE completed" {
		t.Fatalf("DELETE Play: %q", got)
	}
	if got := api.names(); got != "INBOX Play/Q1 Projects Trash" {
		t.Fatalf("folders after DELETE: %q", got)
	}
	if ids := api.holds("Trash"); len(ids) != 0 {
		t.Fatalf("DELETE moved messages to Trash: %v", ids)
	}
	if got := c.tagged("d2", "d2 DELETE Gone"); got != "d2 NO [NONEXISTENT] No such mailbox" {
		t.Fatalf("DELETE of a folder that does not exist: %q", got)
	}

	if got := c.tagged("e1", "e1 RENAME INBOX Old"); got != "e1 OK RENAME completed" {
		t.Fatalf("RENAME INBOX Old: %q", got)
	}
	if ids := api.holds("INBOX"); len(ids) != 0 {
		t.Fatalf("INBOX holds %v after RENAME, want it empty", ids)
	}
	if ids := api.holds("Old"); !slices.Equal(ids, []uint{11, 12}) {
		t.Fatalf("Old holds %v, want INBOX's two messages under new, higher UIDs", ids)
	}
	if out := c.do("e2", "STATUS INBOX (MESSAGES)"); !strings.Contains(out, "(MESSAGES 0)") {
		t.Fatalf("STATUS INBOX after RENAME:\n%s", out)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if slices.Contains(api.calls, "rename INBOX Old") {
		t.Fatalf("the API was asked to rename INBOX: %v", api.calls)
	}
}
