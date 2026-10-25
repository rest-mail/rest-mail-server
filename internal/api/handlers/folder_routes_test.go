package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
)

func folderRoutes(h *MessageHandler) http.Handler {
	r := chi.NewRouter()
	h.RegisterFolders(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()})
	return r
}

func accountFoldersPath(account uint) string {
	return fmt.Sprintf("/api/v1/accounts/%d/folders", account)
}

// The typed answers write the bytes the maps and the local type they replaced wrote: the
// IMAP gateway and the console read them.
func TestFolderResponseJSONParity(t *testing.T) {
	type folderInfo struct {
		Folder string `json:"name"`
		Total  int64  `json:"total"`
		Unread int64  `json:"unread"`
	}
	for _, tc := range []struct {
		name         string
		old, current interface{}
	}{
		{"folder", folderInfo{"INBOX", 3, 1}, FolderSummary{Folder: "INBOX", Total: 3, Unread: 1}},
		{"name", map[string]string{"name": "Receipts"}, FolderName{Name: "Receipts"}},
		{"quota", map[string]interface{}{"quota_bytes": int64(100), "quota_used_bytes": int64(25), "message_count": int64(4), "percent_used": 25.0},
			Quota{MessageCount: 4, PercentUsed: 25, QuotaBytes: 100, QuotaUsedBytes: 25}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, err := json.Marshal(tc.old)
			if err != nil {
				t.Fatal(err)
			}
			current, err := json.Marshal(tc.current)
			if err != nil {
				t.Fatal(err)
			}
			if string(old) != string(current) {
				t.Fatalf("response JSON changed:\nold %s\nnew %s", old, current)
			}
		})
	}
}

// What binding refuses, it refuses before the account is looked up: a nil database proves
// nothing is reached. Bound, a request without a session is the handler's 401.
func TestFolderBindingBeforeDatabase(t *testing.T) {
	routes := folderRoutes(NewMessageHandler(nil, nil, nil, "", nil))
	for _, tc := range []struct{ method, path, body, field string }{
		{http.MethodGet, "/api/v1/accounts/0/folders", "", "id"},
		{http.MethodGet, "/api/v1/accounts/nope/quota", "", "id"},
		{http.MethodPost, "/api/v1/accounts/1/folders", `{}`, "name"},
		{http.MethodPost, "/api/v1/accounts/1/folders", `{"name":12}`, "name"},
		{http.MethodPost, "/api/v1/accounts/1/folders", `{"name":"A","colour":"red"}`, "colour"},
		{http.MethodPatch, "/api/v1/accounts/1/folders/Old", `{"name":null}`, "name"},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			routes.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"`+tc.field+`":`) {
				t.Fatalf("want 422 for %s, got %d %s", tc.field, rr.Code, rr.Body)
			}
		})
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/accounts/1/folders", `{"name":""}`},
		{http.MethodDelete, "/api/v1/accounts/1/folders/INBOX", ""},
		{http.MethodGet, "/api/v1/accounts/1/quota", ""},
	} {
		t.Run("bound "+tc.method+tc.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			routes.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d %s", rr.Code, rr.Body)
			}
		})
	}
}

func messageRoutes(h *MessageHandler) http.Handler {
	r := chi.NewRouter()
	h.RegisterMessages(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()})
	return r
}

func searchRoutes(h *SearchHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()})
	return r
}

func composeRoutes(h *MessageHandler) http.Handler {
	r := chi.NewRouter()
	h.RegisterCompose(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()})
	return r
}
