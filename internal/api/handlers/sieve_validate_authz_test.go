package handlers

// POST /accounts/{id}/sieve/validate is authorized like PUT /accounts/{id}/sieve:
// mail:write on the account (#376, where vertrag's authorization phase found it
// answering 200 to anyone signed in). Reuses the DB harness from
// authz_idor_test.go, so it skips when no test database is reachable.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// validateSieveAs calls ValidateScript on account id with claims and returns
// the status.
func validateSieveAs(h *SieveHandler, claims *auth.Claims, id uint) *httptest.ResponseRecorder {
	r := authzRequest(http.MethodPost, "/sieve/validate", claims.AccountID,
		map[string]string{"id": uintToStr(id)}, `{"script":"keep;"}`)
	r = r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, claims))
	rr := httptest.NewRecorder()
	serveSieve(h, rr, r)
	return rr
}

func TestSieveValidate_AuthorizedLikePut(t *testing.T) {
	db := openAuthzTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })

	fx := seedAuthzFixture(t, tx)
	admin := seedAdminWithCaps(t, tx, "sieveadmin", []string{models.CapMailWrite})
	h := NewSieveHandler(tx)
	owner := fx.ownerMailbox.ID

	refused := []struct {
		name   string
		claims *auth.Claims
	}{
		{"ordinary account, another account's id", &auth.Claims{AccountID: fx.attackerMailbox.ID}},
		{"app password, another account's id", &auth.Claims{AccountID: fx.attackerMailbox.ID, CredentialKind: "app_password"}},
	}
	for _, tc := range refused {
		rr := validateSieveAs(h, tc.claims, owner)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s: expected 403, got %d (%s)", tc.name, rr.Code, rr.Body.String())
		}
	}

	allowed := []struct {
		name   string
		claims *auth.Claims
	}{
		{"owner", &auth.Claims{AccountID: owner}},
		{"owner's app password", &auth.Claims{AccountID: owner, CredentialKind: "app_password"}},
		{"admin with mail:write", &auth.Claims{AccountID: admin.ID}},
	}
	for _, tc := range allowed {
		rr := validateSieveAs(h, tc.claims, owner)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d (%s)", tc.name, rr.Code, rr.Body.String())
		}
	}
}
