package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// enrollWithRecoveryCodes activates TOTP 2FA for the mailbox, with recovery
// codes, as the enroll and confirm endpoints would, and returns the secret and
// the codes.
func enrollWithRecoveryCodes(t *testing.T, gdb *gorm.DB, mailboxID uint, masterKey string) (string, []string) {
	t.Helper()
	key, err := auth.GenerateTOTPSecret(auth.DefaultTOTPIssuer, "recover@example.test")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	enc, err := crypto.EncryptString(key.Secret(), masterKey)
	if err != nil {
		t.Fatalf("EncryptString: %v", err)
	}
	codes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		if hashes[i], err = auth.HashRecoveryCode(c); err != nil {
			t.Fatalf("HashRecoveryCode: %v", err)
		}
	}
	repo := repositories.NewTwoFactorRepository(gdb)
	tf, err := repo.Enroll(mailboxID, enc, hashes)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if err := repo.Confirm(tf.ID); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	return key.Secret(), codes
}

// TestRecover_ResetsThePasswordWithTheAuthenticatorAndARecoveryCode: both
// factors set up in advance reset a forgotten password; the old password and
// every session stop, the recovery code is spent, the authenticator stays.
func TestRecover_ResetsThePasswordWithTheAuthenticatorAndARecoveryCode(t *testing.T) {
	gdb := openLoginTestDB(t)
	const masterKey = "recover-master-key-0123456789abcd"
	h := NewAuthHandler(gdb, auth.NewJWTService("recover-secret", 15*time.Minute, 7*24*time.Hour), masterKey, nil)

	const oldPassword = "correct-horse-battery"
	addr := seedLoginMailbox(t, gdb, oldPassword)
	var before models.Mailbox
	if err := gdb.Where("address = ?", addr).First(&before).Error; err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	secret, codes := enrollWithRecoveryCodes(t, gdb, before.ID, masterKey)

	const newPassword = "a-new-password-nobody-saw"
	rr := post(h.Recover, map[string]string{
		"email": addr, "totp_code": totpNow(t, secret), "recovery_code": codes[0],
		"new_password": newPassword, "client": "native",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("recover: status %d (%s), want 200", rr.Code, rr.Body.String())
	}

	var after models.Mailbox
	if err := gdb.First(&after, before.ID).Error; err != nil {
		t.Fatalf("reload mailbox: %v", err)
	}
	if after.CredentialEpoch <= before.CredentialEpoch {
		t.Errorf("credential epoch %d, want past %d: the old sessions must end", after.CredentialEpoch, before.CredentialEpoch)
	}
	if old := doLogin(h, map[string]string{"email": addr, "password": oldPassword, "totp_code": totpNow(t, secret)}); old.Code != http.StatusUnauthorized {
		t.Errorf("old password: status %d, want 401", old.Code)
	}
	if ok := doLogin(h, map[string]string{"email": addr, "password": newPassword, "totp_code": totpNow(t, secret)}); ok.Code != http.StatusOK {
		t.Errorf("new password with the same authenticator: status %d (%s), want 200", ok.Code, ok.Body.String())
	}

	again := post(h.Recover, map[string]string{
		"email": addr, "totp_code": totpNow(t, secret), "recovery_code": codes[0], "new_password": "yet-another-password",
	})
	if again.Code != http.StatusUnauthorized || errorCode(again) != "recovery_failed" {
		t.Errorf("a spent recovery code: status %d code %q, want 401 recovery_failed", again.Code, errorCode(again))
	}
}

// TestRecover_RefusesWithoutBothFactors: every refusal looks the same, and
// only the recovery code's own check spends it.
func TestRecover_RefusesWithoutBothFactors(t *testing.T) {
	gdb := openLoginTestDB(t)
	const masterKey = "recover-master-key-0123456789abcd"
	h := NewAuthHandler(gdb, auth.NewJWTService("recover-secret", 15*time.Minute, 7*24*time.Hour), masterKey, nil)

	addr := seedLoginMailbox(t, gdb, "correct-horse-battery")
	var mb models.Mailbox
	if err := gdb.Where("address = ?", addr).First(&mb).Error; err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	noTwoFactor := seedLoginMailbox(t, gdb, "correct-horse-battery")
	secret, codes := enrollWithRecoveryCodes(t, gdb, mb.ID, masterKey)

	const newPassword = "a-new-password-nobody-saw"
	refused := []struct {
		name string
		body map[string]string
	}{
		{"unknown address", map[string]string{"email": "nobody-here@example.test", "totp_code": totpNow(t, secret), "recovery_code": codes[0], "new_password": newPassword}},
		{"account without 2FA", map[string]string{"email": noTwoFactor, "totp_code": totpNow(t, secret), "recovery_code": codes[0], "new_password": newPassword}},
		{"wrong authenticator code", map[string]string{"email": addr, "totp_code": "000000", "recovery_code": codes[0], "new_password": newPassword}},
		{"wrong recovery code", map[string]string{"email": addr, "totp_code": totpNow(t, secret), "recovery_code": "not-a-recovery-code", "new_password": newPassword}},
	}
	for _, tc := range refused {
		rr := post(h.Recover, tc.body)
		if rr.Code != http.StatusUnauthorized || errorCode(rr) != "recovery_failed" {
			t.Errorf("%s: status %d code %q (%s), want 401 recovery_failed", tc.name, rr.Code, errorCode(rr), rr.Body.String())
		}
	}

	short := post(h.Recover, map[string]string{"email": addr, "totp_code": totpNow(t, secret), "recovery_code": codes[0], "new_password": "short"})
	if short.Code != http.StatusUnprocessableEntity {
		t.Errorf("short password: status %d, want 422", short.Code)
	}
	missing := post(h.Recover, map[string]string{"email": addr, "recovery_code": codes[0], "new_password": newPassword})
	if missing.Code != http.StatusUnprocessableEntity {
		t.Errorf("no authenticator code: status %d, want 422", missing.Code)
	}

	// None of that spent codes[0].
	rr := post(h.Recover, map[string]string{"email": addr, "totp_code": totpNow(t, secret), "recovery_code": codes[0], "new_password": newPassword})
	if rr.Code != http.StatusOK {
		t.Fatalf("recover after the refusals: status %d (%s), want 200", rr.Code, rr.Body.String())
	}
}

// TestRecover_RefusalsAreBudgetedPerAddress: once an address has used up its
// refusals it is answered 429 without a check, even with both factors right,
// and an unknown address is budgeted the same way; other addresses are not
// affected.
func TestRecover_RefusalsAreBudgetedPerAddress(t *testing.T) {
	gdb := openLoginTestDB(t)
	const masterKey = "recover-master-key-0123456789abcd"
	h := NewAuthHandler(gdb, auth.NewJWTService("recover-secret", 15*time.Minute, 7*24*time.Hour), masterKey, nil)
	h.SetRecoverFailures(middleware.NewFailureBudget(middleware.RateLimitConfig{RPS: 1.0 / 3600, Burst: 2, TTL: 2 * time.Hour}))

	addr := seedLoginMailbox(t, gdb, "correct-horse-battery")
	var mb models.Mailbox
	if err := gdb.Where("address = ?", addr).First(&mb).Error; err != nil {
		t.Fatalf("load mailbox: %v", err)
	}
	other := seedLoginMailbox(t, gdb, "correct-horse-battery")
	secret, codes := enrollWithRecoveryCodes(t, gdb, mb.ID, masterKey)

	const newPassword = "a-new-password-nobody-saw"
	// The budget is the address's however it is spelled.
	for _, email := range []string{addr, "  " + strings.ToUpper(addr) + " "} {
		rr := post(h.Recover, map[string]string{"email": email, "totp_code": "000000", "recovery_code": codes[0], "new_password": newPassword})
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("refusal within the budget: status %d (%s), want 401", rr.Code, rr.Body.String())
		}
	}
	right := post(h.Recover, map[string]string{"email": addr, "totp_code": totpNow(t, secret), "recovery_code": codes[0], "new_password": newPassword})
	if right.Code != http.StatusTooManyRequests || errorCode(right) != "rate_limited" {
		t.Fatalf("both factors right after the budget: status %d code %q, want 429 rate_limited", right.Code, errorCode(right))
	}
	if right.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}

	const unknown = "nobody-here@example.test"
	for i, want := range []int{http.StatusUnauthorized, http.StatusUnauthorized, http.StatusTooManyRequests} {
		rr := post(h.Recover, map[string]string{"email": unknown, "totp_code": "000000", "recovery_code": "nope", "new_password": newPassword})
		if rr.Code != want {
			t.Errorf("unknown address, attempt %d: status %d, want %d", i+1, rr.Code, want)
		}
	}

	rr := post(h.Recover, map[string]string{"email": other, "totp_code": "000000", "recovery_code": "nope", "new_password": newPassword})
	if rr.Code != http.StatusUnauthorized || errorCode(rr) != "recovery_failed" {
		t.Errorf("another address: status %d code %q, want 401 recovery_failed", rr.Code, errorCode(rr))
	}
}
