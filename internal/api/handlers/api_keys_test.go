package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
)

func TestAPIKeyWithinGrants(t *testing.T) {
	global := grants.HeldOf(grants.Entry{Capability: "mailboxes:write", Scope: "*"})
	domain := grants.HeldOf(grants.Entry{Capability: "mailboxes:write", Scope: "domain:example.test"})
	permission := []auth.ScopedPermission{{Capability: "mailboxes:write", Scope: "domain:example.test"}}
	if !apiKeyWithinGrants(global, domain, permission) {
		t.Fatal("a creator may narrow an owner's grant")
	}
	for _, tc := range []struct{ creator, owner grants.Held }{{grants.Held{}, domain}, {global, grants.Held{}}} {
		if apiKeyWithinGrants(tc.creator, tc.owner, permission) {
			t.Fatal("both principals must hold the permission")
		}
	}
	if apiKeyWithinGrants(domain, global, []auth.ScopedPermission{{Capability: "mailboxes:write", Scope: "*"}}) {
		t.Fatal("a domain grant must not delegate installation-wide access")
	}
}

func TestAPIKeyResponseNeverContainsHash(t *testing.T) {
	key := models.Credential{ID: 1, Kind: models.CredentialKindAPIKey, SecretHash: "private-hash", EncryptedSecret: "private-secret",
		Scope: models.CredentialScope{Permissions: []auth.ScopedPermission{{Capability: "domains:read", Scope: "*"}}}}
	body, err := json.Marshal(toAPIKeyResponse(&key))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "private-") || strings.Contains(string(body), "secret_hash") {
		t.Fatal("credential metadata exposed a secret")
	}
	if !strings.Contains(string(body), `"permissions"`) {
		t.Fatal("metadata lost scoped permissions")
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	h := NewCredentialHandler(db)
	id := uintToStr(fx.admin.ID)
	path := "/api/v1/accounts/" + id + "/api-keys"
	request := func(method, target, body string, mfa bool, params map[string]string) *http.Request {
		r := authzRequest(method, target, fx.admin.ID, params, body)
		middleware.GetClaims(r).TwoFactor = mfa
		if !mfa {
			middleware.GetClaims(r).AMR = []string{auth.AMRPassword}
		}
		return r
	}
	createBody := fmt.Sprintf(`{"name":"Provisioning test","password":%q,"permissions":[{"capability":"mailboxes:write","scope":%q}]}`, accountsPassword, grants.Domain(fx.domain.Name))
	noMFA := httptest.NewRecorder()
	credentialRoutes(h).ServeHTTP(noMFA, request(http.MethodPost, path, createBody, false, map[string]string{"id": id}))
	if noMFA.Code != http.StatusForbidden {
		t.Fatalf("without MFA: %d", noMFA.Code)
	}

	created := httptest.NewRecorder()
	credentialRoutes(h).ServeHTTP(created, request(http.MethodPost, path, createBody, true, map[string]string{"id": id}))
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var response struct {
		Data struct {
			apiKeyResponse
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !auth.LooksLikeAPIKey(response.Data.Token) || len(response.Data.Permissions) != 1 || response.Data.ExpiresAt == nil {
		t.Fatal("created key is missing its one-time secret, permissions or expiry")
	}
	if created.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secret response must not be cached")
	}

	listed := httptest.NewRecorder()
	credentialRoutes(h).ServeHTTP(listed, request(http.MethodGet, path, "", true, map[string]string{"id": id}))
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), response.Data.Token) || strings.Contains(listed.Body.String(), `"token"`) {
		t.Fatal("list failed or exposed a key secret")
	}
	repo := repositories.NewCredentialRepository(db)
	credential, owner, err := repo.VerifyAPIKey(response.Data.Token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	claims := repositories.APIKeyClaims(credential, owner)
	if claims.TwoFactor || claims.SessionID != "" {
		t.Fatal("a key must not impersonate an interactive MFA session")
	}

	// A scoped key cannot create accounts without an explicit human-granted
	// machine exception; those exceptions are a later part of #341.
	accounts := NewAccountHandler(db, grants.NewCache(db, grants.CacheTTL), "")
	for _, tc := range []struct {
		address string
		want    int
	}{{"via-key@" + fx.domain.Name, http.StatusForbidden}, {"via-key@unrelated.test", http.StatusForbidden}} {
		r := authzRequest(http.MethodPost, "/api/v1/accounts", fx.admin.ID, nil, fmt.Sprintf(`{"address":%q}`, tc.address))
		*middleware.GetClaims(r) = *claims
		out := httptest.NewRecorder()
		accountRoutes(accounts).ServeHTTP(out, r)
		if out.Code != tc.want {
			t.Fatalf("provisioning %s: %d, want %d", tc.address, out.Code, tc.want)
		}
	}
	// A key cannot use its account's unrelated mail permissions.
	if _, err := mayReach(db, claims, fx.admin.ID, models.CapMailRead); !errors.Is(err, errNoAccess) {
		t.Fatal("key exceeded its explicit permissions")
	}

	deniedBody := fmt.Sprintf(`{"name":"too broad","password":%q,"permissions":[{"capability":"mailboxes:write","scope":"*"}]}`, accountsPassword)
	denied := httptest.NewRecorder()
	credentialRoutes(h).ServeHTTP(denied, request(http.MethodPost, path, deniedBody, true, map[string]string{"id": id}))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("scope escalation: %d", denied.Code)
	}

	revoked := httptest.NewRecorder()
	credentialRoutes(h).ServeHTTP(revoked, request(http.MethodDelete, path+"/"+uintToStr(credential.ID), "", true, map[string]string{"id": id, "credential_id": uintToStr(credential.ID)}))
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", revoked.Code, revoked.Body)
	}
	_, _, err = repo.VerifyAPIKey(response.Data.Token, time.Now())
	var refusal *auth.CredentialRefusal
	if !errors.As(err, &refusal) || refusal.Reason != auth.CredentialRevoked {
		t.Fatal("revoked key was accepted")
	}
}
