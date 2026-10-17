package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
)

// fakeTwoFactorStore is an in-memory twoFactorStore for driving the 2FA handler
// and login-enforcement state machine without a database. It mirrors the real
// repository's semantics over credentials: enrolling revokes what the account
// had, confirm makes a TOTP credential count, GetActive hides pending ones,
// recovery codes are bcrypt-checked and single-use.
type fakeTwoFactorStore struct {
	nextID uint
	creds  []*models.Credential
}

func newFakeStore() *fakeTwoFactorStore {
	return &fakeTwoFactorStore{nextID: 1}
}

func (f *fakeTwoFactorStore) live(accountID uint, kind string) []*models.Credential {
	var out []*models.Credential
	for _, c := range f.creds {
		if c.AccountID == accountID && c.Kind == kind && c.RevokedAt == nil {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTwoFactorStore) Get(accountID uint) (*models.Credential, error) {
	if live := f.live(accountID, models.CredentialKindTOTP); len(live) > 0 {
		return live[len(live)-1], nil
	}
	return nil, repositories.ErrTwoFactorNotFound
}

func (f *fakeTwoFactorStore) GetActive(accountID uint) (*models.Credential, error) {
	active, _ := f.Active(accountID)
	if len(active) > 0 {
		return &active[len(active)-1], nil
	}
	return nil, repositories.ErrTwoFactorNotFound
}

func (f *fakeTwoFactorStore) Active(accountID uint) ([]models.Credential, error) {
	var out []models.Credential
	for _, c := range f.live(accountID, models.CredentialKindTOTP) {
		if c.ConfirmedAt != nil {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (f *fakeTwoFactorStore) Enroll(accountID uint, encryptedSecret string, recoveryHashes []string) (*models.Credential, error) {
	_ = f.Delete(accountID)
	add := func(kind, secret, hash string) *models.Credential {
		c := &models.Credential{ID: f.nextID, AccountID: accountID, Kind: kind, EncryptedSecret: secret, SecretHash: hash}
		f.nextID++
		f.creds = append(f.creds, c)
		return c
	}
	totp := add(models.CredentialKindTOTP, encryptedSecret, "")
	for _, h := range recoveryHashes {
		add(models.CredentialKindRecoveryCode, "", h)
	}
	return totp, nil
}

func (f *fakeTwoFactorStore) Confirm(id uint) error {
	for _, c := range f.creds {
		if c.ID == id && c.Kind == models.CredentialKindTOTP && c.ConfirmedAt == nil && c.RevokedAt == nil {
			now := time.Now()
			c.ConfirmedAt = &now
			return nil
		}
	}
	return repositories.ErrTwoFactorNotFound
}

func (f *fakeTwoFactorStore) Delete(accountID uint) error {
	now := time.Now()
	for _, kind := range []string{models.CredentialKindTOTP, models.CredentialKindRecoveryCode} {
		for _, c := range f.live(accountID, kind) {
			c.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeTwoFactorStore) ConsumeRecoveryCode(accountID uint, plaintext string) (bool, error) {
	for _, c := range f.live(accountID, models.CredentialKindRecoveryCode) {
		if auth.CheckRecoveryCode(plaintext, c.SecretHash) {
			now := time.Now()
			c.RevokedAt = &now
			return true, nil
		}
	}
	return false, nil
}

const testMasterKey = "test-master-key-0123456789abcdef"

func mailboxClaims(id uint, email string) *auth.Claims {
	return &auth.Claims{AccountID: id, Address: email}
}

func withClaims(r *http.Request, c *auth.Claims) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, c))
}

func newTFHandler(store twoFactorStore) *TwoFactorHandler {
	return &TwoFactorHandler{store: store, masterKey: testMasterKey, enabled: true}
}

func doEnroll(t *testing.T, h *TwoFactorHandler, c *auth.Claims) enrollResponse {
	t.Helper()
	req := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/auth/2fa/enroll", nil), c)
	rr := httptest.NewRecorder()
	h.Enroll(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("enroll: status %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data enrollResponse `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode enroll: %v", err)
	}
	return resp.Data
}

func postCode(t *testing.T, fn http.HandlerFunc, c *auth.Claims, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := withClaims(httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(b)), c)
	rr := httptest.NewRecorder()
	fn(rr, req)
	return rr
}

// TestEnrollReturnsSecretAndRecoveryCodesButStaysPending: enrollment hands back
// the provisioning material but does NOT activate 2FA until confirmed.
func TestEnrollReturnsSecretAndRecoveryCodesButStaysPending(t *testing.T) {
	store := newFakeStore()
	h := newTFHandler(store)
	c := mailboxClaims(1, "user@example.test")

	resp := doEnroll(t, h, c)
	if resp.Secret == "" || resp.OTPAuthURL == "" {
		t.Fatalf("enroll response missing secret/url: %+v", resp)
	}
	if len(resp.RecoveryCodes) != auth.RecoveryCodeCount {
		t.Errorf("got %d recovery codes, want %d", len(resp.RecoveryCodes), auth.RecoveryCodeCount)
	}
	// Not active yet: GetActive must miss.
	if _, err := store.GetActive(1); err == nil {
		t.Error("2FA became active before confirmation")
	}
	// Stored secret is encrypted at rest (ciphertext != plaintext, decrypts back).
	row, _ := store.Get(1)
	if row.EncryptedSecret == resp.Secret {
		t.Error("secret stored in plaintext")
	}
	if dec, err := crypto.DecryptString(row.EncryptedSecret, testMasterKey); err != nil || dec != resp.Secret {
		t.Errorf("stored secret does not decrypt to the issued secret: dec=%q err=%v", dec, err)
	}
}

// TestConfirmActivatesWithValidCodeRejectsBad: enrollment is confirmed only by a
// valid first code; a wrong code leaves it pending.
func TestConfirmActivatesWithValidCodeRejectsBad(t *testing.T) {
	store := newFakeStore()
	h := newTFHandler(store)
	c := mailboxClaims(1, "user@example.test")
	resp := doEnroll(t, h, c)

	// Wrong code: rejected, still pending.
	bad := postCode(t, h.Confirm, c, codeRequest{Code: "000000"})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("confirm(bad): status %d, want 401", bad.Code)
	}
	if _, err := store.GetActive(1); err == nil {
		t.Error("2FA active after a rejected confirm")
	}

	// Valid code: activates.
	code, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())
	ok := postCode(t, h.Confirm, c, codeRequest{Code: code})
	if ok.Code != http.StatusNoContent {
		t.Fatalf("confirm(valid): status %d (%s), want 204", ok.Code, ok.Body.String())
	}
	if _, err := store.GetActive(1); err != nil {
		t.Errorf("2FA not active after valid confirm: %v", err)
	}
}

// TestReenrollBlockedWhileActive: once active, re-enroll is refused (must disable
// first), so a live session can't silently rotate the secret.
func TestReenrollBlockedWhileActive(t *testing.T) {
	store := newFakeStore()
	h := newTFHandler(store)
	c := mailboxClaims(1, "user@example.test")
	resp := doEnroll(t, h, c)
	code, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())
	postCode(t, h.Confirm, c, codeRequest{Code: code})

	req := withClaims(httptest.NewRequest(http.MethodPost, "/enroll", nil), c)
	rr := httptest.NewRecorder()
	h.Enroll(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("re-enroll while active: status %d, want 409", rr.Code)
	}
}

// TestDisableRequiresValidSecondFactor: disable needs a valid TOTP or recovery
// code; a bad code leaves 2FA active.
func TestDisableRequiresValidSecondFactor(t *testing.T) {
	store := newFakeStore()
	h := newTFHandler(store)
	c := mailboxClaims(1, "user@example.test")
	resp := doEnroll(t, h, c)
	code, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())
	postCode(t, h.Confirm, c, codeRequest{Code: code})

	// Bad code: refused, still active.
	bad := postCode(t, h.Disable, c, disableRequest{Code: "000000"})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("disable(bad): status %d, want 401", bad.Code)
	}
	if _, err := store.GetActive(1); err != nil {
		t.Error("2FA disabled by an invalid code")
	}

	// Recovery code: accepted, disables.
	ok := postCode(t, h.Disable, c, disableRequest{RecoveryCode: resp.RecoveryCodes[0]})
	if ok.Code != http.StatusNoContent {
		t.Fatalf("disable(recovery): status %d (%s), want 204", ok.Code, ok.Body.String())
	}
	if _, err := store.GetActive(1); err == nil {
		t.Error("2FA still active after a valid disable")
	}
}

// TestRecoveryCodeSingleUse: a recovery code works once, then never again.
func TestRecoveryCodeSingleUse(t *testing.T) {
	store := newFakeStore()
	h := newTFHandler(store)
	c := mailboxClaims(1, "user@example.test")
	resp := doEnroll(t, h, c)
	code, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())
	postCode(t, h.Confirm, c, codeRequest{Code: code})

	rc := resp.RecoveryCodes[0]
	if used, _ := store.ConsumeRecoveryCode(1, rc); !used {
		t.Fatal("first recovery-code use failed")
	}
	if used, _ := store.ConsumeRecoveryCode(1, rc); used {
		t.Error("recovery code reusable — not single-use")
	}
}

// TestEnrollRefusedWhenDisabledOrNoMasterKey: the feature knob off, or no
// encryption key configured, both refuse enrollment (secure-by-construction:
// never store a TOTP secret in plaintext).
func TestEnrollRefusedWhenDisabledOrNoMasterKey(t *testing.T) {
	c := mailboxClaims(1, "user@example.test")

	disabled := &TwoFactorHandler{store: newFakeStore(), masterKey: testMasterKey, enabled: false}
	rr := httptest.NewRecorder()
	disabled.Enroll(rr, withClaims(httptest.NewRequest(http.MethodPost, "/enroll", nil), c))
	if rr.Code != http.StatusForbidden {
		t.Errorf("enroll while disabled: status %d, want 403", rr.Code)
	}

	noKey := &TwoFactorHandler{store: newFakeStore(), masterKey: "", enabled: true}
	rr2 := httptest.NewRecorder()
	noKey.Enroll(rr2, withClaims(httptest.NewRequest(http.MethodPost, "/enroll", nil), c))
	if rr2.Code != http.StatusServiceUnavailable {
		t.Errorf("enroll without MASTER_KEY: status %d, want 503", rr2.Code)
	}
}

// --- login enforcement (AuthHandler.enforce2FA) ---

func enforce(h *AuthHandler, accountID uint, req loginRequest) (bool, *httptest.ResponseRecorder) {
	ok, _, rr := enforceVerified(h, accountID, req)
	return ok, rr
}

func enforceVerified(h *AuthHandler, accountID uint, req loginRequest) (bool, bool, *httptest.ResponseRecorder) {
	rr := httptest.NewRecorder()
	ok, verified := h.enforce2FA(rr, accountID, req)
	return ok, verified, rr
}

// TestLoginEnforcement_NoTwoFactorProceeds: an account without active 2FA passes
// straight through — login is unchanged — and its session signed in with a
// password alone.
func TestLoginEnforcement_NoTwoFactorProceeds(t *testing.T) {
	h := &AuthHandler{twoFactorStore: newFakeStore(), masterKey: testMasterKey}
	ok, verified, _ := enforceVerified(h, 99, loginRequest{})
	if !ok {
		t.Error("account without 2FA was blocked at login")
	}
	if amr := signInMethods(verified); !reflect.DeepEqual(amr, []string{auth.AMRPassword}) {
		t.Errorf("amr = %v, want [pwd]", amr)
	}
}

// TestLoginEnforcement_ActiveRequiresCode: with 2FA active, a missing code is a
// totp_required challenge, a wrong code is rejected, and a valid TOTP or an
// unused recovery code proceeds.
func TestLoginEnforcement_ActiveRequiresCode(t *testing.T) {
	store := newFakeStore()
	tfh := newTFHandler(store)
	c := mailboxClaims(7, "u@example.test")
	resp := doEnroll(t, tfh, c)
	code, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())
	postCode(t, tfh.Confirm, c, codeRequest{Code: code})

	h := &AuthHandler{twoFactorStore: store, masterKey: testMasterKey}

	// Missing code -> blocked with a totp_required challenge.
	ok, rr := enforce(h, 7, loginRequest{})
	if ok {
		t.Error("login proceeded with no second factor")
	}
	if got := errorCode(rr); got != "totp_required" {
		t.Errorf("error code = %q, want totp_required", got)
	}

	// Wrong code -> blocked.
	if ok, _ := enforce(h, 7, loginRequest{TOTPCode: "000000"}); ok {
		t.Error("login proceeded with a wrong code")
	}

	// Valid current code -> proceeds, and the session records the second factor.
	withOTP := []string{auth.AMRPassword, auth.AMROTP}
	valid, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())
	if ok, verified, _ := enforceVerified(h, 7, loginRequest{TOTPCode: valid}); !ok || !reflect.DeepEqual(signInMethods(verified), withOTP) {
		t.Errorf("valid code: ok=%v verified=%v, want a session with %v", ok, verified, withOTP)
	}

	// Recovery code -> proceeds as a second factor (and is consumed: a second
	// use fails).
	if ok, verified, _ := enforceVerified(h, 7, loginRequest{RecoveryCode: resp.RecoveryCodes[0]}); !ok || !reflect.DeepEqual(signInMethods(verified), withOTP) {
		t.Errorf("recovery code: ok=%v verified=%v, want a session with %v", ok, verified, withOTP)
	}
	if ok, _ := enforce(h, 7, loginRequest{RecoveryCode: resp.RecoveryCodes[0]}); ok {
		t.Error("recovery code reusable at login")
	}
}

func errorCode(rr *httptest.ResponseRecorder) string {
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	return resp.Error.Code
}
