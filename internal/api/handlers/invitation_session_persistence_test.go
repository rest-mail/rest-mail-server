package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

const invitationSessionPassword = "chosen fixture first password"

func invitationSessionFixture(t *testing.T) (*gorm.DB, models.Mailbox, string, *auth.JWTService) {
	t.Helper()
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(append(accountModels, &models.OneTimeToken{})...); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "invitation-session.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	account := models.Mailbox{DomainID: domain.ID, LocalPart: "invited", Address: "invited@invitation-session.test", Active: true}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	token, _, err := bootstrap.MintInvitation(t.Context(), db, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	return db, account, token, auth.NewJWTService("invitation-session-fixture", time.Minute, time.Hour)
}

func requestInvitationSession(db *gorm.DB, jwt *auth.JWTService, token string, native bool) *httptest.ResponseRecorder {
	body := map[string]string{"token": token, "password": invitationSessionPassword}
	if native {
		body["client"] = clientNative
	}
	data, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	h := NewInvitationHandler(db, NewAuthHandler(db, jwt, "public-test-master", nil), true)
	invitationRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/auth/invitation", strings.NewReader(string(data))))
	return rr
}

func requireInvitationUnspent(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	var token models.OneTimeToken
	if err := db.Where("account_id = ? AND purpose = ?", id, models.OneTimeTokenInvitation).First(&token).Error; err != nil || token.UsedAt != nil {
		t.Fatalf("failed session consumed invitation: %v", err)
	}
	var account models.Mailbox
	if err := db.First(&account, id).Error; err != nil || account.CredentialEpoch != 0 || account.LastPasswordChange != nil || account.TwoFactorRequired || account.LastLoginAt != nil {
		t.Fatalf("failed session changed account setup: %v", err)
	}
	for _, table := range []any{&models.Credential{}, &models.RefreshToken{}} {
		var count int64
		if err := db.Model(table).Where("account_id = ?", id).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("failed session left credentials or refresh tokens: count%d error%v", count, err)
		}
	}
}

func TestInvitationSessionFailureLeavesRedemptionRetryable(t *testing.T) {
	for _, tc := range []struct {
		fault  string
		native bool
	}{{"ledger", false}, {"ledger", true}, {"enrollment", false}, {"commit", true}} {
		name := tc.fault + "/browser"
		if tc.native {
			name = tc.fault + "/native"
		}
		t.Run(name, func(t *testing.T) {
			db, account, token, jwt := invitationSessionFixture(t)
			const callback = "invitation_session_failure"
			var remove func() error
			switch tc.fault {
			case "ledger":
				if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "refresh_tokens" {
						_ = tx.AddError(errors.New("private invitation ledger detail"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Callback().Create().Remove(callback) }
			case "enrollment":
				if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
					if _, readingCredential := tx.Statement.Dest.(*models.Credential); readingCredential && tx.Statement.Table == "credentials" {
						_ = tx.AddError(errors.New("private invitation enrollment detail"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Callback().Query().Remove(callback) }
			case "commit":
				if err := db.Exec(`CREATE FUNCTION fail_invitation_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'private invitation commit detail'; END $$`).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(`CREATE CONSTRAINT TRIGGER invitation_commit_failure AFTER INSERT ON refresh_tokens
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_invitation_commit()`).Error; err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Exec("DROP TRIGGER invitation_commit_failure ON refresh_tokens").Error }
			}
			t.Cleanup(func() {
				if remove != nil {
					_ = remove()
				}
			})
			rr := requestInvitationSession(db, jwt, token, tc.native)
			if err := remove(); err != nil {
				t.Fatal(err)
			}
			remove = nil
			if rr.Code != http.StatusInternalServerError || strings.Contains(rr.Body.String(), "private") || len(rr.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("session failure exposed success or cookies: status%d", rr.Code)
			}
			requireInvitationUnspent(t, db, account.ID)

			rr = requestInvitationSession(db, jwt, token, tc.native)
			if rr.Code != http.StatusOK || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("same invitation could not be retried: status%d", rr.Code)
			}
			var answer struct{ Data loginResponse }
			if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
				t.Fatal(err)
			}
			access, refresh := answer.Data.AccessToken, answer.Data.RefreshToken
			if !tc.native {
				if access != "" || refresh != "" {
					t.Fatal("browser session exposed body tokens")
				}
				for _, cookie := range rr.Result().Cookies() {
					if cookie.Name == auth.AccessCookieName {
						access = cookie.Value
					}
					if cookie.Name == auth.RefreshCookieName {
						refresh = cookie.Value
					}
				}
			}
			claims, err := jwt.ValidateAccessToken(access)
			if err != nil || claims.AccountID != account.ID || !claims.SetupPending {
				t.Fatal("retry did not issue a setup-only session for the recipient")
			}
			refreshClaims, err := jwt.ValidateRefreshToken(refresh)
			if err != nil {
				t.Fatal("retry has no valid refresh token")
			}
			var ledger models.RefreshToken
			if err := db.Where("jti = ?", refreshClaims.ID).First(&ledger).Error; err != nil || ledger.AccountID != account.ID || ledger.Status != models.RefreshTokenActive {
				t.Fatalf("retry session was not committed: %v", err)
			}
			var credential models.Credential
			if err := db.Where("account_id = ? AND kind = ?", account.ID, models.CredentialKindPassword).First(&credential).Error; err != nil || auth.CheckPassword(invitationSessionPassword, credential.SecretHash) != nil {
				t.Fatalf("retry did not commit the chosen password: %v", err)
			}
			if replay := requestInvitationSession(db, jwt, token, tc.native); replay.Code != http.StatusUnauthorized {
				t.Fatalf("committed invitation was reusable: status%d", replay.Code)
			}
		})
	}
}
