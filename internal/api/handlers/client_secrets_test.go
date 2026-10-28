package handlers

import (
	"encoding/json"
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

// The database-backed lifecycle supplements the database-free verifier and
// exchange tests with creation, grant bounds, audit persistence and revocation.
func TestClientSecretLifecycle(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	handler := NewCredentialHandler(db)
	router := credentialRoutes(handler)
	path := "/api/v1/accounts/" + uintToStr(fx.admin.ID) + "/client-secrets"
	create := func(permission, scope string, mfa bool) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"name":"Invoice robot","password":%q,"permissions":[{"capability":%q,"scope":%q}]}`, accountsPassword, permission, scope)
		req := authzRequest("POST", path, fx.admin.ID, nil, body)
		middleware.GetClaims(req).TwoFactor = mfa
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	if out := create("pipelines:write", grants.Domain(fx.domain.Name), false); out.Code != 403 {
		t.Fatalf("no second factor: %d %s", out.Code, out.Body)
	}
	for _, capability := range []string{"mailboxes:write", "mailbox:manage", "users:write", "audit:read"} {
		if out := create(capability, grants.Domain(fx.domain.Name), true); out.Code != 422 {
			t.Fatalf("tier %s: %d %s", capability, out.Code, out.Body)
		}
	}
	if out := create("pipelines:write", "domain:unheld.test", true); out.Code != 403 {
		t.Fatalf("outside account grants: %d %s", out.Code, out.Body)
	}
	out := create("pipelines:write", grants.Domain(fx.domain.Name), true)
	if out.Code != 201 {
		t.Fatalf("create: %d %s", out.Code, out.Body)
	}
	var result struct {
		Data createdClientSecret `json:"data"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	cred := result.Data
	if cred.Kind != models.CredentialKindClientSecret || cred.ClientID != cred.PublicID || len(cred.ClientSecret) != 43 || len(cred.Permissions) != 1 {
		t.Fatalf("credential %+v", cred)
	}
	var entry models.AuditEntry
	if err := db.Where("action = ? AND target->>'credential_id' = ?", ActionClientSecretCreate, uintToStr(cred.ID)).Take(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.ActorID == nil || *entry.ActorID != fx.admin.ID {
		t.Fatalf("creator audit %+v", entry)
	}
	listed := httptest.NewRecorder()
	router.ServeHTTP(listed, authzRequest("GET", path, fx.admin.ID, nil, ""))
	if listed.Code != 200 || strings.Contains(listed.Body.String(), cred.ClientSecret) || strings.Contains(listed.Body.String(), `"client_secret":`) {
		t.Fatalf("list leaked secret: %d %s", listed.Code, listed.Body)
	}
	repo := repositories.NewCredentialRepository(db)
	if _, _, err := repo.VerifyClientSecret(cred.ClientID, cred.ClientSecret, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := mayReach(db, &auth.Claims{AccountID: fx.admin.ID, CredentialID: cred.ID, CredentialKind: models.CredentialKindClientSecret, APIKeyPermissions: cred.Permissions}, fx.admin.ID, models.CapMailRead); err == nil {
		t.Fatal("credential exceeded its own permissions")
	}
	req := authzRequest("DELETE", path+"/"+uintToStr(cred.ID), fx.admin.ID, nil, "")
	middleware.GetClaims(req).TwoFactor = true
	revoked := httptest.NewRecorder()
	router.ServeHTTP(revoked, req)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", revoked.Code, revoked.Body)
	}
	if _, _, err := repo.VerifyClientSecret(cred.ClientID, cred.ClientSecret, time.Now()); err == nil {
		t.Fatal("revoked secret issued a token")
	}
	if err := repo.MachineTokenVerifier()(&auth.Claims{AccountID: fx.admin.ID, MachineCredentialID: cred.ID}); err == nil {
		t.Fatal("revoked credential's token still works")
	}
}
