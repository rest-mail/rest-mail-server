package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"gorm.io/gorm"
)

const (
	identityPassword  = "Dappled-Kestrel-Faraday-271"
	recoveredPassword = "Golden-Oriole-Lighthouse-819"
)

func identityRouter(t *testing.T, db *gorm.DB) http.Handler {
	t.Helper()
	useRealGrants(t)
	routers := NewRouters(db, auth.NewJWTService("identity-proof-secret", time.Minute*15, time.Hour), &config.Config{
		Environment: "test", TOTP2FAEnabled: true, MasterKey: accessMasterKey,
		RecoveryEmailBaseURL: "https://mail.example.test",
	}, nil)
	t.Cleanup(routers.Close)
	return routers.Public
}

func identityStatus(t *testing.T, rr *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	got := grantErrorCode(rr)
	if rr.Code != status || (code != "" && got != code) {
		t.Fatalf("got %d %q, want %d %q", rr.Code, got, status, code)
	}
}

func identityCall(router http.Handler, path, session string, fields map[string]string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(fields)
	return doRequest(router, http.MethodPost, "/api/v1/auth"+path, session, string(body))
}

func identityLogin(router http.Handler, address, password, totp string) *httptest.ResponseRecorder {
	body := map[string]string{"email": address, "password": password, "client": "native"}
	if totp != "" {
		body["totp_code"] = totp
	}
	return identityCall(router, "/login", "", body)
}

func identitySession(t *testing.T, rr *httptest.ResponseRecorder, address string, steps ...string) apitypes.LoginResponse {
	t.Helper()
	identityStatus(t, rr, http.StatusOK, "")
	var result struct{ Data apitypes.LoginResponse }
	decodeInto(t, rr, &result)
	got := result.Data
	if got.User.Email != address || got.User.ID == 0 || got.AccessToken == "" || got.RefreshToken == "" || !slices.Equal(got.SetupRequired, steps) {
		t.Fatalf("expected native session for %s with setup steps %v, got user %+v and steps %v", address, steps, got.User, got.SetupRequired)
	}
	return got
}

func TestIdentityProvisioningAndRecoverySafeguards(t *testing.T) {
	t.Run("bootstrap_expiry_and_password_retirement", func(t *testing.T) {
		db := openAccessDB(t)
		router := identityRouter(t, db)
		const address = "installer@bootstrap.test"
		now := time.Now()
		outcome, err := bootstrap.EnsureFirstAdmin(db, address, accessPassword, true, now.Add(-bootstrap.BootstrapPasswordLifetime-time.Minute))
		if err != nil || outcome != bootstrap.FirstAdminCreated {
			t.Fatalf("bootstrap outcome = %v, %v", outcome, err)
		}
		identityStatus(t, identityLogin(router, address, accessPassword, ""), http.StatusUnauthorized, "bootstrap_expired")

		// Rearm through the same installer entry point, with a different secret.
		outcome, err = bootstrap.EnsureFirstAdmin(db, address, identityPassword, true, now)
		if err != nil || outcome != bootstrap.FirstAdminRearmed {
			t.Fatalf("rearm outcome = %v, %v", outcome, err)
		}
		login := identitySession(t, identityLogin(router, address, identityPassword, ""), address, "password", "two_factor")
		folders := fmt.Sprintf("/api/v1/accounts/%d/folders", login.User.ID)
		identityStatus(t, doRequest(router, http.MethodGet, folders, login.AccessToken, ""), http.StatusForbidden, "setup_required")
		identityStatus(t, identityCall(router, "/password", login.AccessToken, map[string]string{
			"current_password": identityPassword, "new_password": recoveredPassword,
		}), http.StatusOK, "")
		identityStatus(t, identityLogin(router, address, identityPassword, ""), http.StatusUnauthorized, "unauthorized")
		identityStatus(t, doRequest(router, http.MethodGet, folders, login.AccessToken, ""), http.StatusUnauthorized, "unauthorized")
		identitySession(t, identityLogin(router, address, recoveredPassword, ""), address, "two_factor")
		identityStatus(t, identityCall(router, "/claim", "", map[string]string{"code": "retired-setup-code"}), http.StatusNotFound, "")
	})

	t.Run("invitation_replacement_expiry_replay_and_setup", func(t *testing.T) {
		db := openAccessDB(t)
		router := identityRouter(t, db)
		admin := seedAdmins(t, db, []fixtureAdmin{{name: "inviter", roles: []string{"admin"}, twoFactor: true}})["inviter"]
		session := identitySession(t, identityLogin(router, admin.user.Address, accessPassword, identityTOTP(t, admin.secret)), admin.user.Address)
		target := models.Mailbox{DomainID: admin.user.DomainID, LocalPart: "invited", Address: "invited@access.test", Active: true}
		if err := db.Create(&target).Error; err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/api/v1/admin/mailboxes/%d/invitation", target.ID)
		issue := func() string {
			t.Helper()
			rr := doRequest(router, http.MethodPost, path, session.AccessToken, "")
			identityStatus(t, rr, http.StatusCreated, "")
			var result struct {
				Data struct {
					Token     string
					ExpiresAt time.Time `json:"expires_at"`
				}
			}
			decodeInto(t, rr, &result)
			if result.Data.Token == "" || !result.Data.ExpiresAt.After(time.Now()) {
				t.Fatal("invitation response must contain a live secret")
			}
			return result.Data.Token
		}
		accept := func(token string) *httptest.ResponseRecorder {
			return identityCall(router, "/invitation", "", map[string]string{"token": token, "password": identityPassword, "client": "native"})
		}
		replaced, expired := issue(), issue()
		identityStatus(t, accept(replaced), http.StatusUnauthorized, "invalid_token")
		expireIdentityToken(t, db, expired)
		identityStatus(t, accept(expired), http.StatusUnauthorized, "invalid_token")
		identityPasswordMatches(t, db, target.ID, identityPassword, false)

		token := issue()
		identityStatus(t, identityCall(router, "/reset", "", map[string]string{
			"token": token, "password": recoveredPassword, "client": "native",
		}), http.StatusUnauthorized, "invalid_token")
		login := identitySession(t, accept(token), target.Address, "two_factor")
		if login.User.ID != target.ID {
			t.Fatalf("invitation signed in account %d, want %d", login.User.ID, target.ID)
		}
		requireSpentIdentityToken(t, db, token, models.OneTimeTokenInvitation, target.ID)
		identityPasswordMatches(t, db, target.ID, identityPassword, true)
		folders := fmt.Sprintf("/api/v1/accounts/%d/folders", target.ID)
		identityStatus(t, doRequest(router, http.MethodGet, folders, login.AccessToken, ""), http.StatusForbidden, "setup_required")
		identityStatus(t, accept(token), http.StatusUnauthorized, "invalid_token")
		identityStatus(t, doRequest(router, http.MethodPost, path, session.AccessToken, ""), http.StatusConflict, "account_not_invitable")
	})

	t.Run("verified_recovery_is_bound_to_the_account_and_preserves_mfa", func(t *testing.T) {
		db := openAccessDB(t)
		router := identityRouter(t, db)
		accounts := seedAdmins(t, db, []fixtureAdmin{{name: "recovering", twoFactor: true}, {name: "independent"}})
		recovering, independent := accounts["recovering"], accounts["independent"]
		signedIn := identitySession(t, identityLogin(router, recovering.user.Address, accessPassword, identityTOTP(t, recovering.secret)), recovering.user.Address)
		otherSession := identitySession(t, identityLogin(router, independent.user.Address, accessPassword, ""), independent.user.Address)
		const backup = "disposable@external.test"
		const otherBackup = "pending@external.test"
		register := func(session, backup, password string) *httptest.ResponseRecorder {
			return identityCall(router, "/recovery-email", session, map[string]string{"email": backup, "current_password": password})
		}
		request := func(address string) *httptest.ResponseRecorder {
			return identityCall(router, "/recover/email", "", map[string]string{"email": address})
		}
		verify := func(token string) *httptest.ResponseRecorder {
			return identityCall(router, "/recovery-email/verify", "", map[string]string{"token": token})
		}
		redeem := func(token string) *httptest.ResponseRecorder {
			return identityCall(router, "/recover/email/redeem", "", map[string]string{"token": token, "new_password": recoveredPassword})
		}
		t.Run("wrong_current_password_cannot_register_a_method", func(t *testing.T) {
			identityStatus(t, register(otherSession.AccessToken, backup, identityPassword), http.StatusForbidden, "invalid_password")
		})
		identityStatus(t, register(otherSession.AccessToken, otherBackup, accessPassword), http.StatusOK, "")
		identityStatus(t, register(signedIn.AccessToken, backup, accessPassword), http.StatusOK, "")
		verification := queuedIdentityToken(t, db, backup, "verify", 1)
		t.Run("unverified_independent_account_receives_no_reset_link", func(t *testing.T) {
			unknown, pending := request("unknown@access.test"), request(independent.user.Address)
			identityStatus(t, unknown, http.StatusOK, "")
			identityStatus(t, pending, http.StatusOK, "")
			if unknown.Body.String() != pending.Body.String() {
				t.Fatal("pending and unknown account had different responses")
			}
			queuedIdentityToken(t, db, otherBackup, "verify", 1)
			var tokens int64
			if err := db.Model(&models.OneTimeToken{}).
				Where("account_id = ? AND purpose = ?", independent.user.ID, models.OneTimeTokenRecoverEmail).
				Count(&tokens).Error; err != nil || tokens != 0 {
				t.Fatalf("unverified account has %d reset tokens, err %v, want zero", tokens, err)
			}
		})
		identityStatus(t, redeem(verification), http.StatusUnauthorized, "invalid_token")
		identityStatus(t, verify(verification), http.StatusOK, "")
		requireSpentIdentityToken(t, db, verification, models.OneTimeTokenVerifyRecoveryEmail, recovering.user.ID)
		identityStatus(t, verify(verification), http.StatusUnauthorized, "invalid_token")

		identityStatus(t, request(recovering.user.Address), http.StatusOK, "")
		expired := queuedIdentityToken(t, db, backup, "recover", 2)
		expireIdentityToken(t, db, expired)
		t.Run("expired_recovery_link_cannot_change_credentials", func(t *testing.T) {
			identityStatus(t, redeem(expired), http.StatusUnauthorized, "invalid_token")
			identityPasswordMatches(t, db, recovering.user.ID, recoveredPassword, false)
		})
		identityStatus(t, request(recovering.user.Address), http.StatusOK, "")
		token := queuedIdentityToken(t, db, backup, "recover", 3)

		// Even with the other mailbox's signed-in session on the request, the
		// link changes the account that enrolled and verified the method.
		rr := identityCall(router, "/recover/email/redeem", otherSession.AccessToken, map[string]string{"token": token, "new_password": recoveredPassword})
		identityStatus(t, rr, http.StatusOK, "")
		if len(rr.Result().Cookies()) != 0 || strings.Contains(rr.Body.String(), "access_token") {
			t.Fatal("recovery redemption issued a session")
		}
		requireSpentIdentityToken(t, db, token, models.OneTimeTokenRecoverEmail, recovering.user.ID)
		identityStatus(t, redeem(token), http.StatusUnauthorized, "invalid_token")
		identityPasswordMatches(t, db, recovering.user.ID, accessPassword, false)
		identityPasswordMatches(t, db, independent.user.ID, recoveredPassword, false)
		ownFolders := fmt.Sprintf("/api/v1/accounts/%d/folders", recovering.user.ID)
		otherFolders := fmt.Sprintf("/api/v1/accounts/%d/folders", independent.user.ID)
		identityStatus(t, doRequest(router, http.MethodGet, ownFolders, signedIn.AccessToken, ""), http.StatusUnauthorized, "unauthorized")
		identityStatus(t, doRequest(router, http.MethodGet, otherFolders, otherSession.AccessToken, ""), http.StatusOK, "")
		identitySession(t, identityLogin(router, independent.user.Address, accessPassword, ""), independent.user.Address)
		identityStatus(t, identityLogin(router, recovering.user.Address, recoveredPassword, ""), http.StatusUnauthorized, "totp_required")
		restored := identitySession(t, identityLogin(router, recovering.user.Address, recoveredPassword, identityTOTP(t, recovering.secret)), recovering.user.Address)
		identityStatus(t, doRequest(router, http.MethodGet, ownFolders, restored.AccessToken, ""), http.StatusOK, "")
	})
}

func identityTOTP(t *testing.T, secret string) string {
	t.Helper()
	code, err := auth.GenerateTOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func expireIdentityToken(t *testing.T, db *gorm.DB, token string) {
	t.Helper()
	res := db.Model(&models.OneTimeToken{}).Where("token_hash = ?", fmt.Sprintf("%x", sha256.Sum256([]byte(token)))).
		Update("expires_at", time.Now().Add(-time.Minute))
	if res.Error != nil || res.RowsAffected != 1 {
		t.Fatalf("expire token: changed %d rows, err %v", res.RowsAffected, res.Error)
	}
}

func requireSpentIdentityToken(t *testing.T, db *gorm.DB, token, purpose string, accountID uint) {
	t.Helper()
	var row models.OneTimeToken
	if err := db.Where("token_hash = ? AND account_id = ? AND purpose = ?", fmt.Sprintf("%x", sha256.Sum256([]byte(token))), accountID, purpose).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.UsedAt == nil {
		t.Fatalf("the %s link for account %d was not spent", purpose, accountID)
	}
}

func identityPasswordMatches(t *testing.T, db *gorm.DB, id uint, password string, want bool) {
	t.Helper()
	cred, err := repositories.NewCredentialRepository(db).MatchPassword(id, password, time.Now())
	if err != nil || (cred != nil) != want {
		t.Fatalf("account %d password match = %t, err %v, want %t", id, cred != nil, err, want)
	}
}

func queuedIdentityToken(t *testing.T, db *gorm.DB, recipient, purpose string, expected int64) string {
	t.Helper()
	var n int64
	query := db.Model(&models.OutboundQueue{}).Where("recipient = ?", recipient)
	if err := query.Count(&n).Error; err != nil || n != expected {
		t.Fatalf("queued mail for %s = %d, err %v, want %d", recipient, n, err, expected)
	}
	var row models.OutboundQueue
	if err := query.Order("id DESC").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	_, link, ok := strings.Cut(row.RawMessage, "https://mail.example.test/api/v1/auth/recovery-email/link?mode="+purpose+"#")
	secret, _, ended := strings.Cut(link, "\r\n")
	if !ok || !ended || len(secret) != 43 {
		t.Fatalf("mail to %s did not contain a %s link with a private fragment", recipient, purpose)
	}
	return secret
}
