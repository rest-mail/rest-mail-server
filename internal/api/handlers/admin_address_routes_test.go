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

// These helpers exercise the registered routes while allowing handler tests to supply
// their own authorization context. Scope integration tests exercise the real checks.
func addressTestPass(string) endpoint.Middleware {
	return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
}
func mailboxRoutes(h *MailboxHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, MailboxRouteChecks{OnList: addressTestPass, OnMailbox: addressTestPass})
	return r
}
func aliasRoutes(h *AliasHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, AliasRouteChecks{OnList: addressTestPass, OnAlias: addressTestPass})
	return r
}

// Loaded associations retain their fields and null logins. Unloaded or redacted
// associations are omitted instead of serializing invalid zero-filled records.
func TestAdminAddressResponseJSONParity(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	domain := models.Domain{ID: 2, Name: "example.test", DKIMPrivateKey: "secret-key", DKIMSelector: "mail", CreatedAt: now}
	mailbox := models.Mailbox{ID: 3, DomainID: 2, Address: "a@example.test", Domain: domain,
		LastLoginAt: &now, LastPasswordChange: &now, CredentialEpoch: 9, BootstrapExpiresAt: &now,
		QuotaUsage: &models.QuotaUsage{MailboxID: 3, SubjectBytes: 4, UpdatedAt: now}}
	alias := models.Alias{ID: 4, DomainID: 2, Domain: domain, SourceAddress: "alias@example.test", DestinationAddress: "remote@example.test"}
	loadedMailbox := mailbox
	loadedMailbox.QuotaUsage = nil
	loadedQuota := models.QuotaUsage{MailboxID: mailbox.ID, SubjectBytes: 4, UpdatedAt: now, Mailbox: loadedMailbox}
	for _, tc := range []struct {
		name         string
		old, current interface{}
	}{
		{"mailbox loaded", mailbox, mailboxOf(mailbox)},
		{"mailbox redacted", models.Mailbox{}, mailboxOf(models.Mailbox{})},
		{"quota mailbox loaded", loadedQuota, quotaUsageOf(&loadedQuota)},
		{"alias loaded", alias, aliasOf(alias)},
		{"alias created or redacted", models.Alias{}, aliasOf(models.Alias{})},
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
			var want map[string]interface{}
			if err := json.Unmarshal(old, &want); err != nil {
				t.Fatal(err)
			}
			if tc.name == "alias created or redacted" {
				delete(want, "domain")
			}
			if strings.HasPrefix(tc.name, "mailbox") {
				if tc.name == "mailbox redacted" {
					delete(want, "domain")
				}
				if quota, ok := want["quota_usage"].(map[string]interface{}); ok {
					delete(quota, "mailbox")
				}
			}
			var got map[string]interface{}
			if err := json.Unmarshal(current, &got); err != nil {
				t.Fatal(err)
			}
			wanted, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(wanted) != string(actual) {
				t.Fatalf("response JSON differs:\nwant %s\ngot %s", wanted, actual)
			}
			if strings.Contains(string(current), "secret-key") || strings.Contains(string(current), "credential_epoch") || strings.Contains(string(current), "bootstrap_expires_at") {
				t.Fatalf("secret leaked: %s", current)
			}
		})
	}
}

func TestAdminAddressBindingBeforeDatabase(t *testing.T) {
	for _, tc := range []struct {
		method, path, body, field string
		handler                   http.Handler
	}{
		{http.MethodGet, "/api/v1/admin/mailboxes/0", "", "id", mailboxRoutes(NewMailboxHandler(nil, nil, nil))},
		{http.MethodDelete, "/api/v1/admin/mailboxes/nope/sessions", "", "id", mailboxRoutes(NewMailboxHandler(nil, nil, nil))},
		{http.MethodGet, "/api/v1/admin/aliases?domain_id=nope", "", "domain_id", aliasRoutes(NewAliasHandler(nil))},
		{http.MethodPatch, "/api/v1/admin/mailboxes/1", `{"domain_id":2}`, "domain_id", mailboxRoutes(NewMailboxHandler(nil, nil, nil))},
		{http.MethodPatch, "/api/v1/admin/aliases/1", `{"source_address":"forged@example.test"}`, "source_address", aliasRoutes(NewAliasHandler(nil))},
	} {
		t.Run(tc.method+tc.path+tc.field, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"`+tc.field+`":`) {
				t.Fatalf("want 422 for %s, got %d %s", tc.field, rr.Code, rr.Body)
			}
		})
	}
}
