package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

// A draft saved without a from goes to the caller's own mailbox, and only when
// the credential holds mail:write on it (#1278). The check used to run only
// for an explicit from, so an API key holding mail:read alone could create
// drafts.
func TestSaveDraft_WithoutFromNeedsMailWriteOnTheCallersMailbox(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedAuthzFixture(t, tx)
	h := NewMessageHandler(tx, nil, nil, "", nil)
	own := grants.Mailbox(fx.ownerMailbox.Address)
	key := func(capabilities ...string) *auth.Claims {
		claims := &auth.Claims{AccountID: fx.ownerMailbox.ID, CredentialID: 9, CredentialKind: auth.CredentialKindAPIKey}
		for _, c := range capabilities {
			claims.APIKeyPermissions = append(claims.APIKeyPermissions, auth.ScopedPermission{Capability: c, Scope: own})
		}
		return claims
	}
	drafts := func() int64 {
		t.Helper()
		var n int64
		if err := tx.Model(&models.Message{}).Where("mailbox_id = ? AND is_draft", fx.ownerMailbox.ID).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	save := func(claims *auth.Claims) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		composeRoutes(h).ServeHTTP(rr, withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/messages/draft", strings.NewReader(`{}`)), claims))
		return rr
	}

	if rr := save(key(models.CapMailRead)); rr.Code != http.StatusForbidden {
		t.Fatalf("a key holding mail:read only: want 403, got %d: %s", rr.Code, rr.Body)
	}
	if drafts() != 0 {
		t.Fatal("the refused draft was stored")
	}
	if rr := save(key(models.CapMailRead, models.CapMailWrite)); rr.Code != http.StatusCreated {
		t.Fatalf("a key holding mail:write: want 201, got %d: %s", rr.Code, rr.Body)
	}
	// The account itself holds the owner's capabilities on its own mailbox.
	if rr := save(&auth.Claims{AccountID: fx.ownerMailbox.ID}); rr.Code != http.StatusCreated {
		t.Fatalf("the account's own session: want 201, got %d: %s", rr.Code, rr.Body)
	}
	if drafts() != 2 {
		t.Errorf("want 2 drafts, got %d", drafts())
	}
}
