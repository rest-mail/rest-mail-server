package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/crypto"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// twoFactorStore is the enrollment/verification ledger the 2FA and login flows
// need (OSI-19). A consumer-side interface (implemented by
// repositories.TwoFactorRepository) so the handler state machine can be
// unit-tested against an in-memory fake with no database.
type twoFactorStore interface {
	Get(accountID uint) (*models.Credential, error)
	GetActive(accountID uint) (*models.Credential, error)
	Active(accountID uint) ([]models.Credential, error)
	Enroll(accountID uint, encryptedSecret string, recoveryHashes []string) (*models.Credential, error)
	Confirm(id uint) error
	Delete(accountID uint) error
	ConsumeRecoveryCode(accountID uint, plaintext string) (bool, error)
}

// TwoFactorHandler serves the authenticated 2FA management endpoints:
// enroll → confirm → disable, plus a status read. Every endpoint keys on the
// caller's own JWT claims, so an account can only ever manage its own 2FA.
type TwoFactorHandler struct {
	db        *gorm.DB
	store     twoFactorStore
	masterKey string
	// enabled mirrors cfg.TOTP2FAEnabled. When false, new enrollment/confirm is
	// refused (the feature is turned off for this deployment); it does NOT
	// weaken verification for already-active enrollments — see the config note.
	enabled bool
	// sessions is the cache RejectStaleSessions reads; turning 2FA off ends
	// the account's sessions, and empties its entry.
	sessions *SessionCache
}

func NewTwoFactorHandler(db *gorm.DB, masterKey string, enabled bool, sessions *SessionCache) *TwoFactorHandler {
	var store twoFactorStore
	if db != nil {
		store = repositories.NewTwoFactorRepository(db)
	}
	return &TwoFactorHandler{db: db, store: store, masterKey: masterKey, enabled: enabled, sessions: sessions}
}

// twoFactorOwner is the account a session's 2FA requests act on: its own, and
// its address, which labels the authenticator entry. Only a session: a
// credential sent with HTTP Basic never reaches these routes.
func twoFactorOwner(c *auth.Claims) (accountID uint, label string, ok bool) {
	if c == nil || c.AccountID == 0 || c.CredentialID != 0 {
		return 0, "", false
	}
	return c.AccountID, c.Address, true
}

type twoFactorStatusResponse struct {
	// Enabled is true when 2FA is active (a confirmed enrollment gates login).
	Enabled bool `json:"enabled"`
	// Pending is true when an enrollment exists but has not been confirmed yet.
	Pending bool `json:"pending"`
}

// Status reports whether the caller has 2FA active and/or a pending enrollment.
// GET /api/v1/auth/2fa
func (h *TwoFactorHandler) Status(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := twoFactorOwner(middleware.GetClaims(r))
	if !ok {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	tf, err := h.store.Get(accountID)
	if errors.Is(err, repositories.ErrTwoFactorNotFound) {
		respond.Data(w, http.StatusOK, twoFactorStatusResponse{})
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to read 2FA status")
		return
	}
	respond.Data(w, http.StatusOK, twoFactorStatusResponse{Enabled: tf.ConfirmedAt != nil, Pending: tf.ConfirmedAt == nil})
}

type enrollResponse struct {
	// Secret is the base32 TOTP secret, for manual entry into an authenticator.
	Secret string `json:"secret"`
	// OTPAuthURL is the otpauth:// provisioning URI (QR-code payload).
	OTPAuthURL string `json:"otpauth_url"`
	// RecoveryCodes are the one-time recovery codes, returned ONCE at enrollment
	// (only their hashes are stored). The client must surface these to the user.
	RecoveryCodes []string `json:"recovery_codes"`
}

// Enroll begins 2FA enrollment: it mints a TOTP secret + recovery codes, stores
// the secret encrypted-at-rest and the recovery codes hashed, and returns the
// provisioning URI + plaintext recovery codes. The enrollment is PENDING until
// Confirm verifies a first code, so a mistyped secret can never lock the user
// out. Re-enrolling replaces any prior pending enrollment.
// POST /api/v1/auth/2fa/enroll
func (h *TwoFactorHandler) Enroll(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		respond.Error(w, http.StatusForbidden, "forbidden", "Two-factor authentication is disabled on this server")
		return
	}
	// Secrets MUST be stored encrypted at rest; refuse rather than silently
	// persist a TOTP secret in plaintext when no encryption key is configured.
	if h.masterKey == "" {
		respond.Error(w, http.StatusServiceUnavailable, "unavailable", "Two-factor authentication requires server encryption to be configured")
		return
	}
	accountID, label, ok := twoFactorOwner(middleware.GetClaims(r))
	if !ok {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	// A confirmed enrollment must be explicitly disabled before re-enrolling, so
	// an attacker holding a live session can't silently swap the secret out from
	// under an active 2FA user. A pending enrollment may be freely replaced.
	if existing, err := h.store.Get(accountID); err == nil && existing.ConfirmedAt != nil {
		respond.Error(w, http.StatusConflict, "conflict", "Two-factor authentication is already enabled; disable it first")
		return
	} else if err != nil && !errors.Is(err, repositories.ErrTwoFactorNotFound) {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to read 2FA status")
		return
	}

	key, err := auth.GenerateTOTPSecret(auth.DefaultTOTPIssuer, label)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to generate 2FA secret")
		return
	}
	encrypted, err := crypto.EncryptString(key.Secret(), h.masterKey)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to secure 2FA secret")
		return
	}

	recoveryCodes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to generate recovery codes")
		return
	}
	hashes := make([]string, len(recoveryCodes))
	for i, c := range recoveryCodes {
		hash, herr := hashRecoveryCode(c)
		if herr != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to secure recovery codes")
			return
		}
		hashes[i] = hash
	}

	if _, err := h.store.Enroll(accountID, encrypted, hashes); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save 2FA enrollment")
		return
	}

	respond.Data(w, http.StatusOK, enrollResponse{
		Secret:        key.Secret(),
		OTPAuthURL:    key.URL(),
		RecoveryCodes: recoveryCodes,
	})
}

type codeRequest struct {
	Code string `json:"code"`
}

// Confirm completes enrollment by verifying a first TOTP code against the
// pending secret, flipping the enrollment active. Until this succeeds, 2FA does
// not gate login.
// POST /api/v1/auth/2fa/confirm
func (h *TwoFactorHandler) Confirm(w http.ResponseWriter, r *http.Request) {
	if !h.enabled {
		respond.Error(w, http.StatusForbidden, "forbidden", "Two-factor authentication is disabled on this server")
		return
	}
	accountID, _, ok := twoFactorOwner(middleware.GetClaims(r))
	if !ok {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	var req codeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Code == "" {
		respond.ValidationError(w, map[string]string{"code": "required"})
		return
	}

	tf, err := h.store.Get(accountID)
	if errors.Is(err, repositories.ErrTwoFactorNotFound) {
		respond.Error(w, http.StatusBadRequest, "bad_request", "No pending 2FA enrollment to confirm")
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to read 2FA enrollment")
		return
	}
	if tf.ConfirmedAt != nil {
		respond.Error(w, http.StatusConflict, "conflict", "Two-factor authentication is already enabled")
		return
	}

	secret, err := crypto.DecryptString(tf.EncryptedSecret, h.masterKey)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to read 2FA secret")
		return
	}
	if !auth.ValidateTOTPCode(req.Code, secret) {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid 2FA code")
		return
	}
	if err := h.store.Confirm(tf.ID); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to enable 2FA")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type disableRequest struct {
	Code         string `json:"code"`
	RecoveryCode string `json:"recovery_code"`
}

// Disable turns 2FA off. It requires proof of possession — a valid current TOTP
// code OR an unused recovery code — so a hijacked-but-2FA-protected session
// can't strip the protection without the second factor.
// POST /api/v1/auth/2fa/disable
func (h *TwoFactorHandler) Disable(w http.ResponseWriter, r *http.Request) {
	accountID, _, ok := twoFactorOwner(middleware.GetClaims(r))
	if !ok {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	var req disableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	_, err := h.store.GetActive(accountID)
	if errors.Is(err, repositories.ErrTwoFactorNotFound) {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Two-factor authentication is not enabled")
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to read 2FA enrollment")
		return
	}

	if !h.verifySecondFactor(accountID, req.Code, req.RecoveryCode) {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid 2FA code")
		return
	}
	// Where 2FA is required (the bootstrap admin), turning it off would make the
	// requirement a one-time gate rather than a rule.
	if h.db != nil {
		var account models.Mailbox
		if err := h.db.Select("two_factor_required").First(&account, accountID).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to read account")
			return
		}
		if account.TwoFactorRequired {
			respond.Error(w, http.StatusForbidden, "two_factor_required", "Two-factor authentication is required for this account and cannot be disabled")
			return
		}
	}
	if err := h.disable(accountID); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to disable 2FA")
		return
	}
	h.sessions.Forget(accountID)
	w.WriteHeader(http.StatusNoContent)
}

// disable revokes the account's authenticator and recovery codes and, in the
// same transaction, ends every session of the account, this one included: a
// session the second factor vouched for must not outlive it. Its app
// passwords are credentials of their own, and stay. Without a database (unit
// tests on a fake store) there are no sessions to end.
func (h *TwoFactorHandler) disable(accountID uint) error {
	if h.db == nil {
		return h.store.Delete(accountID)
	}
	return h.db.Transaction(func(tx *gorm.DB) error {
		if err := repositories.NewTwoFactorRepository(tx).Delete(accountID); err != nil {
			return err
		}
		return endSessions(tx, accountID)
	})
}

// verifySecondFactor checks a TOTP code or a recovery code for the account.
func (h *TwoFactorHandler) verifySecondFactor(accountID uint, totpCode, recoveryCode string) bool {
	return verifyTOTPOrRecovery(h.store, h.masterKey, accountID, totpCode, recoveryCode)
}

// verifyTOTPOrRecovery reports whether totpCode is a current code of one of
// the account's confirmed TOTP credentials, or recoveryCode one of its unspent
// recovery codes, which it spends. A TOTP code is tried first; a recovery code
// only when no TOTP code matched, so one is never spent needlessly.
func verifyTOTPOrRecovery(store twoFactorStore, masterKey string, accountID uint, totpCode, recoveryCode string) bool {
	if totpCode != "" && masterKey != "" {
		creds, err := store.Active(accountID)
		if err == nil {
			for i := range creds {
				secret, err := crypto.DecryptString(creds[i].EncryptedSecret, masterKey)
				if err == nil && auth.ValidateTOTPCode(totpCode, secret) {
					return true
				}
			}
		}
	}
	if recoveryCode != "" {
		if consumed, err := store.ConsumeRecoveryCode(accountID, recoveryCode); err == nil && consumed {
			return true
		}
	}
	return false
}
