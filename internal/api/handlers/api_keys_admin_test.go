package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// The administrator's view of API keys: every key the caller's scope covers, never
// its secret, never a revoked key or another kind, and each filter narrowing it.
func TestListAdminAPIKeys(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)

	now := time.Now()
	older, later := now.Add(-time.Hour), now.Add(time.Hour)
	key := func(account models.Mailbox, name, kind, capability string, lastUsed, expires, revoked *time.Time) {
		t.Helper()
		row := models.Credential{AccountID: account.ID, Kind: kind, Name: name, PublicID: "pub" + name, SecretHash: "secret-hash-" + name,
			Scope: models.CredentialScope{MailboxID: account.ID, Capabilities: []string{capability},
				Permissions: []auth.ScopedPermission{{Capability: capability, Scope: grants.Mailbox(account.Address)}}},
			LastUsedAt: lastUsed, ExpiresAt: expires, RevokedAt: revoked}
		if err := tx.Create(&row).Error; err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	key(fx.ownerMailbox, "reader", models.CredentialKindAPIKey, models.CapMailRead, &later, &later, nil)
	key(fx.attackerMailbox, "writer", models.CredentialKindAPIKey, models.CapMailWrite, &older, &older, nil)
	key(fx.ownerMailbox, "revoked", models.CredentialKindAPIKey, models.CapMailRead, nil, nil, &older)
	key(fx.ownerMailbox, "phone", models.CredentialKindAppPassword, models.CapMailRead, nil, nil, nil)

	routes := chiRouterFor(NewCredentialHandler(tx))
	list := func(query string) (names []string, body string) {
		t.Helper()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/api-keys"+query, nil)
		req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, freshTwoFactorClaims(fx.ownerMailbox.ID, "")))
		routes.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", query, rr.Code, rr.Body)
		}
		var page struct {
			Data []struct {
				Name           string `json:"name"`
				AccountAddress string `json:"account_address"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Data {
			names = append(names, item.Name)
		}
		return names, rr.Body.String()
	}

	names, body := list("")
	if strings.Join(names, ",") != "writer,reader" && strings.Join(names, ",") != "reader,writer" {
		t.Fatalf("listed %v, want the two unrevoked API keys", names)
	}
	if strings.Contains(body, "secret-hash") || !strings.Contains(body, fx.ownerMailbox.Address) {
		t.Fatalf("response: %s", body)
	}
	for query, want := range map[string]string{
		"?owner=" + fx.ownerMailbox.Address:                   "reader",
		"?capability=" + models.CapMailWrite:                  "writer",
		"?last_used_before=" + now.UTC().Format(time.RFC3339): "writer",
		"?expires_before=" + now.UTC().Format(time.RFC3339):   "writer",
	} {
		if names, _ := list(query); strings.Join(names, ",") != want {
			t.Errorf("%s listed %v, want %s", query, names, want)
		}
	}
}

// A domain-scoped administrator sees only keys of accounts in its own domains.
func TestListAdminAPIKeysStaysInsideTheCallersScope(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)

	other := models.Domain{Name: "elsewhere.test"}
	if err := tx.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	stranger := models.Mailbox{DomainID: other.ID, LocalPart: "stranger", Address: "stranger@elsewhere.test"}
	if err := tx.Create(&stranger).Error; err != nil {
		t.Fatal(err)
	}
	for _, account := range []models.Mailbox{fx.ownerMailbox, stranger} {
		row := models.Credential{AccountID: account.ID, Kind: models.CredentialKindAPIKey, Name: account.LocalPart, PublicID: "pub" + account.LocalPart,
			Scope: models.CredentialScope{MailboxID: account.ID, Capabilities: []string{models.CapMailRead}}}
		if err := tx.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}

	router := chi.NewRouter()
	NewCredentialHandler(tx).RegisterAdmin(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, func(capability string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: middleware.RequireCapabilityOnList(func(uint) (grants.Held, error) {
			return grants.HeldOf(grants.Entry{Scope: grants.Domain("idor.test"), Capability: capability}), nil
		}, capability)}
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/api-keys", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, freshTwoFactorClaims(fx.ownerMailbox.ID, "")))
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), fx.ownerMailbox.Address) || strings.Contains(rr.Body.String(), stranger.Address) {
		t.Fatalf("scoped list: %s", rr.Body)
	}
}

func chiRouterFor(h *CredentialHandler) http.Handler {
	router := chi.NewRouter()
	h.RegisterAdmin(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, func(string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	})
	return router
}
