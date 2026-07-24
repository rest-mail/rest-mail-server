package handlers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// enroll2FAForMailbox activates TOTP 2FA for the seeded mailbox (encrypting the
// secret exactly as the enroll endpoint would) and returns the base32 secret.
func enroll2FAForMailbox(t *testing.T, gdb *gorm.DB, mailboxID uint, masterKey string) string {
	t.Helper()
	key, err := auth.GenerateTOTPSecret(auth.DefaultTOTPIssuer, "login-2fa@example.test")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	enc, err := crypto.EncryptString(key.Secret(), masterKey)
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	repo := repositories.NewTwoFactorRepository(gdb)
	tf, err := repo.Enroll("mailbox", mailboxID, enc, nil)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if err := repo.Confirm(tf.ID); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete("mailbox", mailboxID) })
	return key.Secret()
}

// TestLogin_2FAActive_RequiresCode: OSI-19 end-to-end through the real Login
// handler. With 2FA active, a correct password alone is challenged
// (totp_required); a correct password plus a valid TOTP code succeeds.
func TestLogin_2FAActive_RequiresCode(t *testing.T) {
	gdb := openLoginTestDB(t)
	if err := gdb.AutoMigrate(&models.TwoFactor{}, &models.TwoFactorRecoveryCode{}); err != nil {
		t.Skipf("2FA login test skipped: migrate failed (%v)", err)
	}
	const masterKey = "login-2fa-master-key-0123456789ab"
	jwtSvc := auth.NewJWTService("login-secret", 15*time.Minute, 7*24*time.Hour)
	h := NewAuthHandler(gdb, jwtSvc, masterKey)

	const password = "correct-horse-battery"
	addr := seedLoginMailbox(t, gdb, password)

	var mb models.Mailbox
	if err := gdb.Where("address = ?", addr).First(&mb).Error; err != nil {
		t.Fatalf("load seeded mailbox: %v", err)
	}
	secret := enroll2FAForMailbox(t, gdb, mb.ID, masterKey)

	// Correct password, no code -> 401 totp_required (not tokens).
	noCode := doLogin(h, map[string]string{"email": addr, "password": password})
	if noCode.Code != http.StatusUnauthorized {
		t.Fatalf("password-only login: status %d, want 401", noCode.Code)
	}
	if got := errorCode(noCode); got != "totp_required" {
		t.Errorf("password-only login error code = %q, want totp_required", got)
	}

	// Correct password, wrong code -> 401.
	wrong := doLogin(h, map[string]string{"email": addr, "password": password, "totp_code": "000000"})
	if wrong.Code != http.StatusUnauthorized {
		t.Errorf("wrong-code login: status %d, want 401", wrong.Code)
	}

	// Correct password + valid code -> 200 with an access token.
	code, err := auth.GenerateTOTPCode(secret, time.Now())
	if err != nil {
		t.Fatalf("GenerateTOTPCode: %v", err)
	}
	ok := doLogin(h, map[string]string{"email": addr, "password": password, "totp_code": code})
	if ok.Code != http.StatusOK {
		t.Fatalf("valid-code login: status %d (%s), want 200", ok.Code, ok.Body.String())
	}
	var resp struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(ok.Body.Bytes(), &resp); err != nil || resp.Data.AccessToken == "" {
		t.Errorf("valid-code login did not return an access token: %v", err)
	}
}

// TestLogin_NoTwoFactor_Unchanged: a mailbox without 2FA logs in with just a
// password even when the handler is 2FA-capable — non-2FA accounts are unchanged.
func TestLogin_NoTwoFactor_Unchanged(t *testing.T) {
	gdb := openLoginTestDB(t)
	if err := gdb.AutoMigrate(&models.TwoFactor{}, &models.TwoFactorRecoveryCode{}); err != nil {
		t.Skipf("2FA login test skipped: migrate failed (%v)", err)
	}
	jwtSvc := auth.NewJWTService("login-secret", 15*time.Minute, 7*24*time.Hour)
	h := NewAuthHandler(gdb, jwtSvc, "login-2fa-master-key-0123456789ab")

	const password = "correct-horse-battery"
	addr := seedLoginMailbox(t, gdb, password)

	rr := doLogin(h, map[string]string{"email": addr, "password": password})
	if rr.Code != http.StatusOK {
		t.Fatalf("non-2FA login: status %d (%s), want 200", rr.Code, rr.Body.String())
	}
}
