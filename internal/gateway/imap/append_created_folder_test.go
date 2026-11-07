package imap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

func TestAPPENDToCreatedFolderPreservesImportedFlags(t *testing.T) {
	for _, flags := range []string{`\Seen`, `\Flagged \Seen`, "", "$Forwarded $Junk", `\Deleted \Seen $Forwarded`} {
		t.Run(flags, func(t *testing.T) {
			api := newFakeAPI()
			var mutex sync.Mutex
			folders := map[string]bool{"INBOX": true}
			mux := http.NewServeMux()
			mux.Handle("/", api.handler())
			mux.HandleFunc("GET /api/v1/accounts/7/folders", func(w http.ResponseWriter, r *http.Request) {
				mutex.Lock()
				defer mutex.Unlock()
				response := apiclient.FolderListResponse{}
				for name := range folders {
					response.Data = append(response.Data, apiclient.Folder{Folder: name})
				}
				writeJSON(w, response)
			})
			mux.HandleFunc("POST /api/v1/accounts/7/folders", func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Name string `json:"name"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Name != "Cutover" {
					http.Error(w, "invalid folder", http.StatusUnprocessableEntity)
					return
				}
				mutex.Lock()
				defer mutex.Unlock()
				if folders[request.Name] {
					http.Error(w, "already exists", http.StatusConflict)
					return
				}
				folders[request.Name] = true
				w.WriteHeader(http.StatusCreated)
				writeJSON(w, map[string]any{"data": request})
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			backend := NewBackend(apiclient.New(srv.URL), nil)
			creator := newTranscript(t, backend)
			creator.login("login")
			if _, status := creator.command("create", "CREATE Cutover"); status != "create OK CREATE completed" {
				t.Fatal(status)
			}
			session := newTranscript(t, backend)
			session.login("login")
			lines, status := session.command("list", `LIST "" "Cutover"`)
			if status != "list OK LIST completed" || !slices.Contains(lines, `* LIST () "/" "Cutover"`) {
				t.Fatalf("created folder absent in another session: %q %s", lines, status)
			}
			raw := "Message-ID: <cutover@example.test>\r\nSubject: cutover\r\n\r\nbody\r\n"
			session.send(`append APPEND Cutover (%s) "07-Nov-2026 15:01:32 +0000" {%d}`, flags, len(raw))
			if continuation := session.readLine(); !strings.HasPrefix(continuation, "+") {
				t.Fatal(continuation)
			}
			session.writeLine(raw + "\r\n")
			if _, status := session.readResponse("append"); status != "append OK [APPENDUID 1 100] APPEND completed" {
				t.Fatal(status)
			}
			request := api.lastImport(t)
			gotFlags, wantFlags := slices.Clone(request.Flags), strings.Fields(flags)
			slices.Sort(gotFlags)
			slices.Sort(wantFlags)
			if request.Folder != "Cutover" || !slices.Equal(gotFlags, wantFlags) {
				t.Fatalf("import folder=%q flags=%q, want Cutover %q", request.Folder, request.Flags, wantFlags)
			}
		})
	}
}

func TestCREATERefusesAnAPIStorageFailure(t *testing.T) {
	api := newFakeAPI()
	mux := http.NewServeMux()
	mux.Handle("/", api.handler())
	mux.HandleFunc("GET /api/v1/accounts/7/folders", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, apiclient.FolderListResponse{Data: []apiclient.Folder{{Folder: "INBOX"}}})
	})
	mux.HandleFunc("POST /api/v1/accounts/7/folders", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	session := newTranscript(t, NewBackend(apiclient.New(srv.URL), nil))
	session.login("login")
	if _, status := session.command("create", "CREATE Cutover"); status != "create NO CREATE failed" {
		t.Fatalf("failed storage acknowledged: %s", status)
	}
}
