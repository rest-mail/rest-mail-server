package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
)

func contactRoutes(h *ContactHandler) http.Handler {
	r := chi.NewRouter()
	h.RegisterContacts(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()})
	return r
}

// contactsPathOf is the account's contacts, followed by suffix.
func contactsPathOf(account uint, suffix string) string {
	return fmt.Sprintf("/api/v1/accounts/%d/contacts%s", account, suffix)
}

// The typed answers write the bytes the model and the map they replaced wrote.
func TestContactResponseJSONParity(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	row := models.Contact{ID: 3, MailboxID: 4, Email: "a@example.test", Name: "A", TrustLevel: "trusted",
		Source: "manual", CreatedAt: now, UpdatedAt: now, Mailbox: models.Mailbox{Address: "owner@example.test"}}
	for _, tc := range []struct {
		name         string
		old, current interface{}
	}{
		{"contact", row, contactOf(row)},
		{"empty contact", models.Contact{}, contactOf(models.Contact{})},
		{"imported", map[string]int{"imported": 2}, ContactsImported{Imported: 2}},
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
// nothing is reached. A name and a trust level left out are not required.
func TestContactBindingBeforeDatabase(t *testing.T) {
	routes := contactRoutes(NewContactHandler(nil))
	for _, tc := range []struct{ method, path, body, field string }{
		{http.MethodGet, "/api/v1/accounts/0/contacts", "", "id"},
		{http.MethodGet, "/api/v1/accounts/1/contacts?trust_level=friend", "", "trust_level"},
		{http.MethodGet, "/api/v1/accounts/1/contacts?limit=ten", "", "limit"},
		{http.MethodPost, "/api/v1/accounts/1/contacts", `{"name":"No address"}`, "email"},
		{http.MethodPost, "/api/v1/accounts/1/contacts", `{"email":"a@example.test","colour":"red"}`, "colour"},
		{http.MethodPatch, "/api/v1/accounts/1/contacts/1", `{"trust_level":"friend"}`, "trust_level"},
		{http.MethodPatch, "/api/v1/accounts/1/contacts/nope", `{}`, "cid"},
		{http.MethodDelete, "/api/v1/accounts/1/contacts/0", "", "cid"},
		{http.MethodPost, "/api/v1/accounts/1/contacts/block", `{"email":5}`, "email"},
		{http.MethodPost, "/api/v1/accounts/1/contacts/import", `{"contacts":[{"name":"No address"}]}`, "contacts[0].email"},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			routes.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"`+tc.field+`":`) {
				t.Fatalf("want 422 for %s, got %d %s", tc.field, rr.Code, rr.Body)
			}
		})
	}
	// Bound, these reach the account check, which a request with no session fails.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/accounts/1/contacts", `{"email":"a@example.test"}`},
		{http.MethodPatch, "/api/v1/accounts/1/contacts/1", `{"name":null}`},
		{http.MethodPost, "/api/v1/accounts/1/contacts/import", `{"contacts":[{"email":"a@example.test"}]}`},
		{http.MethodGet, "/api/v1/accounts/1/contacts/suggest?q=", ""},
	} {
		t.Run("bound "+tc.method+tc.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			routes.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusForbidden {
				t.Fatalf("want 403 forbidden, got %d %s", rr.Code, rr.Body)
			}
		})
	}
}
