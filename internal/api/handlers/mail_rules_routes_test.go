package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
)

// routeAtAccount serves r, built by authzRequest for a suffix such as "/sieve", through
// register's routes at the account its id parameter names.
func routeAtAccount(register func(endpoint.Routes), w http.ResponseWriter, r *http.Request) {
	routes := chi.NewRouter()
	register(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()})
	r.URL.Path = "/api/v1/accounts/" + chi.RouteContext(r.Context()).URLParam("id") + r.URL.Path
	routes.ServeHTTP(w, r)
}

func serveSieve(h *SieveHandler, w http.ResponseWriter, r *http.Request) {
	routeAtAccount(h.Register, w, r)
}

func serveVacation(h *VacationHandler, w http.ResponseWriter, r *http.Request) {
	routeAtAccount(h.Register, w, r)
}

// The typed answers write what the model and the maps they replaced wrote. With no script
// stored, the answer is the three fields the map wrote.
func TestMailRulesResponseJSONParity(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	stored := models.SieveScript{ID: 3, MailboxID: 4, Script: "keep;", ScriptHash: "abc", ValidatedAt: &now,
		Active: true, CreatedAt: now, UpdatedAt: now}
	unhashed := models.SieveScript{ID: 5, MailboxID: 4, Script: "keep;", CreatedAt: now, UpdatedAt: now}
	for _, tc := range []struct {
		name         string
		old, current interface{}
		sameOrder    bool
	}{
		{"stored script", stored, sieveScriptOf(stored), true},
		{"script stored before hashing", unhashed, sieveScriptOf(unhashed), true},
		{"no script", map[string]interface{}{"mailbox_id": 4, "script": "", "active": false}, SieveScript{MailboxID: 4}, false},
		{"deleted", map[string]string{"status": "deleted"}, SieveDeleted{Status: "deleted"}, true},
		{"valid", map[string]interface{}{"valid": true, "message": "Script is valid"}, SieveValidation{Message: "Script is valid", Valid: true}, true},
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
			if tc.sameOrder {
				if string(old) != string(current) {
					t.Fatalf("response JSON changed:\nold %s\nnew %s", old, current)
				}
				return
			}
			var oldValue, currentValue map[string]interface{}
			if err := json.Unmarshal(old, &oldValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(current, &currentValue); err != nil {
				t.Fatal(err)
			}
			if len(oldValue) != len(currentValue) {
				t.Fatalf("fields changed:\nold %s\nnew %s", old, current)
			}
			for k, v := range oldValue {
				if currentValue[k] != v {
					t.Fatalf("%s changed:\nold %s\nnew %s", k, old, current)
				}
			}
		})
	}
}

// What binding refuses, it refuses before the account is looked up: a nil database proves
// nothing is reached.
func TestMailRulesBindingBeforeDatabase(t *testing.T) {
	sieve := chi.NewRouter()
	NewSieveHandler(nil).Register(endpoint.Routes{Router: sieve, Registry: endpoint.NewRegistry()})
	NewVacationHandler(nil).Register(endpoint.Routes{Router: sieve, Registry: endpoint.NewRegistry()})
	for _, tc := range []struct{ method, path, body, field string }{
		{http.MethodGet, "/api/v1/accounts/0/vacation", "", "id"},
		{http.MethodPut, "/api/v1/accounts/1/vacation", `{"subject":"Away"}`, "enabled"},
		{http.MethodPut, "/api/v1/accounts/1/vacation", `{"enabled":"yes"}`, "enabled"},
		{http.MethodPut, "/api/v1/accounts/1/vacation", `{"enabled":false,"start_date":"tomorrow"}`, "start_date"},
		{http.MethodPut, "/api/v1/accounts/1/vacation", `{"enabled":false,"reply_to":"x"}`, "reply_to"},
		{http.MethodDelete, "/api/v1/accounts/nope/sieve", "", "id"},
		{http.MethodPut, "/api/v1/accounts/1/sieve", `{"active":true}`, "script"},
		{http.MethodPut, "/api/v1/accounts/1/sieve", `{"script":7}`, "script"},
		{http.MethodPost, "/api/v1/accounts/1/sieve/validate", `{}`, "script"},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			sieve.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"`+tc.field+`":`) {
				t.Fatalf("want 422 for %s, got %d %s", tc.field, rr.Code, rr.Body)
			}
		})
	}
	// Bound, these reach the account check, which a request with no session fails.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/accounts/1/vacation", `{"enabled":null}`},
		{http.MethodPut, "/api/v1/accounts/1/sieve", `{"script":null}`},
		{http.MethodGet, "/api/v1/accounts/1/sieve", ""},
	} {
		t.Run("bound "+tc.method+tc.path+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			sieve.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusForbidden {
				t.Fatalf("want 403 forbidden, got %d %s", rr.Code, rr.Body)
			}
		})
	}
}
