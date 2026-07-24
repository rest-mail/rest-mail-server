package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
// repository's semantics: at most one enrollment per owner, confirm flips
// active, GetActive hides pending rows, recovery codes are bcrypt-checked and
// single-use.
type fakeTwoFactorStore struct {
	nextID   uint
	rows     []*models.TwoFactor
	recovery map[uint][]*models.TwoFactorRecoveryCode // by TwoFactorID
}

func newFakeStore() *fakeTwoFactorStore {
	return &fakeTwoFactorStore{nextID: 1, recovery: map[uint][]*models.TwoFactorRecoveryCode{}}
}

func (f *fakeTwoFactorStore) find(userType string, subjectID uint) *models.TwoFactor {
	for _, r := range f.rows {
		if r.UserType == userType && r.SubjectID == subjectID {
			return r
		}
	}
	return nil
}

func (f *fakeTwoFactorStore) Get(userType string, subjectID uint) (*models.TwoFactor, error) {
	if r := f.find(userType, subjectID); r != nil {
		return r, nil
	}
	return nil, repositories.ErrTwoFactorNotFound
}

func (f *fakeTwoFactorStore) GetActive(userType string, subjectID uint) (*models.TwoFactor, error) {
	if r := f.find(userType, subjectID); r != nil && r.Confirmed {
		return r, nil
	}
	return nil, repositories.ErrTwoFactorNotFound
}

func (f *fakeTwoFactorStore) Enroll(userType string, subjectID uint, encryptedSecret string, recoveryHashes []string) (*models.TwoFactor, error) {
	// Replace any prior enrollment for this owner.
	kept := f.rows[:0]
	for _, r := range f.rows {
		if r.UserType == userType && r.SubjectID == subjectID {
			delete(f.recovery, r.ID)
			continue
		}
		kept = append(kept, r)
	}
	f.rows = kept

	row := &models.TwoFactor{ID: f.nextID, UserType: userType, SubjectID: subjectID, EncryptedSecret: encryptedSecret}
	f.nextID++
	f.rows = append(f.rows, row)
	for _, h := range recoveryHashes {
		f.recovery[row.ID] = append(f.recovery[row.ID], &models.TwoFactorRecoveryCode{TwoFactorID: row.ID, CodeHash: h})
	}
	return row, nil
}

func (f *fakeTwoFactorStore) Confirm(id uint) error {
	for _, r := range f.rows {
		if r.ID == id && !r.Confirmed {
			r.Confirmed = true
			now := time.Now()
			r.ConfirmedAt = &now
			return nil
		}
	}
	return repositories.ErrTwoFactorNotFound
}

func (f *fakeTwoFactorStore) Delete(userType string, subjectID uint) error {
	kept := f.rows[:0]
	for _, r := range f.rows {
		if r.UserType == userType && r.SubjectID == subjectID {
			delete(f.recovery, r.ID)
			continue
		}
		kept = append(kept, r)
	}
	f.rows = kept
	return nil
}

func (f *fakeTwoFactorStore) ConsumeRecoveryCode(twoFactorID uint, plaintext string) (bool, error) {
	for _, rc := range f.recovery[twoFactorID] {
		if rc.UsedAt == nil && auth.CheckRecoveryCode(plaintext, rc.CodeHash) {
			now := time.Now()
			rc.UsedAt = &now
			return true, nil
		}
	}
	return false, nil
}

const testMasterKey = "test-master-key-0123456789abcdef"

func mailboxClaims(id uint, email string) *auth.Claims {
	return &auth.Claims{UserType: "mailbox", MailboxID: id, Email: email}
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
	if _, err := store.GetActive("mailbox", 1); err == nil {
		t.Error("2FA became active before confirmation")
	}
	// Stored secret is encrypted at rest (ciphertext != plaintext, decrypts back).
	row, _ := store.Get("mailbox", 1)
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
	if _, err := store.GetActive("mailbox", 1); err == nil {
		t.Error("2FA active after a rejected confirm")
	}

	// Valid code: activates.
	code, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())
	ok := postCode(t, h.Confirm, c, codeRequest{Code: code})
	if ok.Code != http.StatusNoContent {
		t.Fatalf("confirm(valid): status %d (%s), want 204", ok.Code, ok.Body.String())
	}
	if _, err := store.GetActive("mailbox", 1); err != nil {
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
	if _, err := store.GetActive("mailbox", 1); err != nil {
		t.Error("2FA disabled by an invalid code")
	}

	// Recovery code: accepted, disables.
	ok := postCode(t, h.Disable, c, disableRequest{RecoveryCode: resp.RecoveryCodes[0]})
	if ok.Code != http.StatusNoContent {
		t.Fatalf("disable(recovery): status %d (%s), want 204", ok.Code, ok.Body.String())
	}
	if _, err := store.GetActive("mailbox", 1); err == nil {
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

	row, _ := store.Get("mailbox", 1)
	rc := resp.RecoveryCodes[0]
	if used, _ := store.ConsumeRecoveryCode(row.ID, rc); !used {
		t.Fatal("first recovery-code use failed")
	}
	if used, _ := store.ConsumeRecoveryCode(row.ID, rc); used {
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

func enforce(h *AuthHandler, userType string, subjectID uint, req loginRequest) (bool, *httptest.ResponseRecorder) {
	rr := httptest.NewRecorder()
	ok := h.enforce2FA(rr, userType, subjectID, req)
	return ok, rr
}

// TestLoginEnforcement_NoTwoFactorProceeds: an account without active 2FA passes
// straight through — login is unchanged.
func TestLoginEnforcement_NoTwoFactorProceeds(t *testing.T) {
	h := &AuthHandler{twoFactorStore: newFakeStore(), masterKey: testMasterKey}
	if ok, _ := enforce(h, "mailbox", 99, loginRequest{}); !ok {
		t.Error("account without 2FA was blocked at login")
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
	ok, rr := enforce(h, "mailbox", 7, loginRequest{})
	if ok {
		t.Error("login proceeded with no second factor")
	}
	if got := errorCode(rr); got != "totp_required" {
		t.Errorf("error code = %q, want totp_required", got)
	}

	// Wrong code -> blocked.
	if ok, _ := enforce(h, "mailbox", 7, loginRequest{TOTPCode: "000000"}); ok {
		t.Error("login proceeded with a wrong code")
	}

	// Valid current code -> proceeds.
	valid, _ := auth.GenerateTOTPCode(resp.Secret, time.Now())
	if ok, _ := enforce(h, "mailbox", 7, loginRequest{TOTPCode: valid}); !ok {
		t.Error("login blocked despite a valid code")
	}

	// Recovery code -> proceeds (and is consumed: a second use fails).
	if ok, _ := enforce(h, "mailbox", 7, loginRequest{RecoveryCode: resp.RecoveryCodes[0]}); !ok {
		t.Error("login blocked despite a valid recovery code")
	}
	if ok, _ := enforce(h, "mailbox", 7, loginRequest{RecoveryCode: resp.RecoveryCodes[0]}); ok {
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
