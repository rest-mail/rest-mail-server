package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

func TestFreshTwoFactorCredentialCreation(t *testing.T) {
	db := openAccountsTestDB(t)
	fx := seedAccountsFixture(t, db)
	h := credentialRoutes(NewCredentialHandler(db))
	path := fmt.Sprintf("/api/v1/accounts/%d/credentials", fx.admin.ID)
	body := fmt.Sprintf(`{"name":"fresh proof","password":%q}`, accountsPassword)
	for _, tc := range []struct {
		name    string
		age     time.Duration
		machine bool
		status  int
		code    string
	}{
		{"fresh", time.Minute, false, 201, ""},
		{"stale", 16 * time.Minute, false, 403, "recent_two_factor_required"},
		{"machine", time.Minute, true, 403, "two_factor_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := freshTwoFactorClaims(fx.admin.ID, fx.admin.Address)
			claims.AuthTime = jwt.NewNumericDate(time.Now().Add(-tc.age))
			if tc.machine {
				claims.CredentialKind = "app_password"
				claims.CredentialID = 42
			}
			var before int64
			db.Model(&models.Credential{}).Where("account_id = ?", fx.admin.ID).Count(&before)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, withClaims(httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)), claims))
			if rr.Code != tc.status || tc.code != "" && errorCode(rr) != tc.code {
				t.Fatalf("got %d %s", rr.Code, rr.Body)
			}
			var after int64
			db.Model(&models.Credential{}).Where("account_id = ?", fx.admin.ID).Count(&after)
			want := before
			if tc.status == 201 {
				want++
			}
			if after != want {
				t.Fatalf("credential count %d, want %d", after, want)
			}
		})
	}
}

func TestFreshTwoFactorGrantCapabilities(t *testing.T) {
	db, actor := grantingTx(t, openAccountsTestDB(t))
	actor.AuthTime = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	target := seedMailbox(t, db, "fresh-grant-target")
	h := grantRoutes(NewGrantHandler(db, grants.NewCache(db, time.Minute), ""))
	for _, tc := range []struct {
		capability string
		fresh      bool
		status     int
	}{
		{"mail:write", false, 201},
		{"mail:read", false, 403},
		{"mail:send", false, 403},
		{"domains:read", false, 403},
		{"mail:read", true, 201},
	} {
		t.Run(fmt.Sprintf("%s/fresh=%t", tc.capability, tc.fresh), func(t *testing.T) {
			role := models.Role{Name: "fresh-" + revUniq()}
			if err := db.Create(&role).Error; err != nil {
				t.Fatal(err)
			}
			// Exactly the capability under test: which ones a grant needs a
			// fresh second factor for is what this checks.
			setExactRoleCaps(t, db, role.ID, []string{tc.capability})
			c := *actor
			if tc.fresh {
				c.AuthTime = jwt.NewNumericDate(time.Now())
			}
			body := fmt.Sprintf(`{"holder":{"kind":"account","id":%d},"role":%q,"scope":%q}`, target.ID, role.Name, grants.Mailbox(target.Address))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, withClaims(httptest.NewRequest("POST", "/api/v1/admin/grants", strings.NewReader(body)), &c))
			if rr.Code != tc.status {
				t.Fatalf("got %d %s", rr.Code, rr.Body)
			}
			if tc.status == 403 && errorCode(rr) != "recent_two_factor_required" {
				t.Fatalf("wrong refusal %s", rr.Body)
			}
			var count int64
			db.Model(&models.Grant{}).Where("role_id = ?", role.ID).Count(&count)
			if (count == 1) != (tc.status == 201) {
				t.Fatalf("unexpected persisted grant count %d", count)
			}
		})
	}
}

func TestSensitiveHandlersRefuseStaleProofBeforeStorage(t *testing.T) {
	claims := freshTwoFactorClaims(1, "a@example.test")
	claims.AuthTime = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	for _, tc := range []struct {
		name, method, path, body string
		handler                  http.Handler
	}{
		{"credential", "POST", "/api/v1/accounts/1/credentials", `{"name":"client","password":"current"}`, credentialRoutes(NewCredentialHandler(nil))},
		{"link", "POST", "/api/v1/accounts/link", `{"address":"b@example.test","password":"current"}`, accountRoutes(NewAccountHandler(nil, nil, ""))},
		{"2fa", "POST", "/api/v1/auth/2fa/disable", `{"totp_code":"123456"}`, twoFactorRoutes(newTFHandler(newFakeStore()))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler.ServeHTTP(rr, withClaims(httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)), claims))
			if rr.Code != 403 || errorCode(rr) != "recent_two_factor_required" {
				t.Fatalf("got %d %s", rr.Code, rr.Body)
			}
		})
	}
	// Forged second-factor markers on machine credentials never make a person.
	for _, kind := range []string{"app_password", auth.CredentialKindAPIKey, "client_secret", "key_pair"} {
		c := *claims
		c.CredentialKind = kind
		c.AuthTime = jwt.NewNumericDate(time.Now())
		r := withClaims(httptest.NewRequest("POST", "/", nil), &c)
		if err := RequireFreshTwoFactor(r); err == nil {
			t.Errorf("accepted %s", kind)
		}
	}
}
