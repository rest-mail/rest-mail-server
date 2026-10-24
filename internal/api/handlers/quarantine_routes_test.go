package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

func quarantineRoutes(h *PipelineHandler) http.Handler {
	r := chi.NewRouter()
	h.RegisterQuarantine(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()})
	return r
}

// quarantinePathOf is the account's quarantine, or one message in it when mid is not 0,
// followed by suffix.
func quarantinePathOf(account, mid uint, suffix string) string {
	path := fmt.Sprintf("/api/v1/accounts/%d/quarantine", account)
	if mid != 0 {
		path += fmt.Sprintf("/%d", mid)
	}
	return path + suffix
}

// quarantineRequest is a request from a session signed in to account.
func quarantineRequest(method string, account uint, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	return r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: account}))
}

// Ids and paging are bound before the account is looked up: a nil database proves nothing
// is reached.
func TestQuarantineBindingBeforeDatabase(t *testing.T) {
	routes := quarantineRoutes(NewPipelineHandler(nil, nil, filters.JSSidecar{}))
	for _, tc := range []struct{ method, path, field string }{
		{http.MethodGet, "/api/v1/accounts/0/quarantine", "id"},
		{http.MethodGet, "/api/v1/accounts/nope/quarantine", "id"},
		{http.MethodGet, "/api/v1/accounts/1/quarantine?limit=ten", "limit"},
		{http.MethodGet, "/api/v1/accounts/1/quarantine?offset=-x", "offset"},
		{http.MethodPost, "/api/v1/accounts/1/quarantine/0/release", "mid"},
		{http.MethodDelete, "/api/v1/accounts/1/quarantine/nope", "mid"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			routes.ServeHTTP(rr, quarantineRequest(tc.method, 1, tc.path))
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"`+tc.field+`":`) {
				t.Fatalf("want 422 for %s, got %d %s", tc.field, rr.Code, rr.Body)
			}
		})
	}
}

// Without a session the account is not reachable: 403, as before, and nothing is looked up.
func TestQuarantineWithoutClaimsIsForbidden(t *testing.T) {
	routes := quarantineRoutes(NewPipelineHandler(nil, nil, filters.JSSidecar{}))
	rr := httptest.NewRecorder()
	routes.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/1/quarantine", nil))
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), `"forbidden"`) {
		t.Fatalf("want 403 forbidden, got %d %s", rr.Code, rr.Body)
	}
}

// The typed answers write the bytes the model and the maps they replaced wrote, and never the
// raw message.
func TestQuarantineResponseJSONParity(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	score := float32(7.5)
	held := models.Quarantine{ID: 3, MailboxID: 4, Sender: "s@example.test", Subject: "hi", BodyPreview: "b",
		RawMessage: "secret-raw", SpamScore: &score, QuarantineReason: "spam", ReceivedAt: now, ExpiresAt: now}
	for _, tc := range []struct {
		name         string
		old, current interface{}
	}{
		{"held", held, quarantinedMessageOf(held)},
		{"held without a score", models.Quarantine{}, quarantinedMessageOf(models.Quarantine{})},
		{"released", map[string]string{"status": "released", "message_id": "12"}, QuarantineReleased{MessageID: "12", Status: "released"}},
		{"deleted", map[string]string{"status": "deleted"}, QuarantineDeleted{Status: "deleted"}},
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
			if strings.Contains(string(current), "secret-raw") {
				t.Fatalf("raw message answered: %s", current)
			}
		})
	}
}
