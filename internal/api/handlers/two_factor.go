package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
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

// RegisterOwn registers the session's own two-factor routes, which a session still in setup
// reaches. checks are login's throttle, for the routes that make or check a code, and the
// audit log; the status read carries no secret to guess and is left unthrottled.
func (h *TwoFactorHandler) RegisterOwn(rs endpoint.Routes, checks SessionRouteChecks) {
	tags := []string{"Auth"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/auth/2fa",
		OperationID: "getTwoFactorStatus", Summary: "Read your authenticator status", Tags: tags}, h.status)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/2fa/enroll",
		OperationID: "enrollTwoFactor", Summary: "Start an authenticator enrollment", Tags: tags,
		Errors:     []int{http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable},
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.2fa_enroll")}}, h.enroll)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/2fa/confirm",
		OperationID: "confirmTwoFactor", Summary: "Switch on a pending authenticator with its first code", Tags: tags,
		Status: http.StatusNoContent, Errors: []int{http.StatusForbidden, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.2fa_confirm")}}, h.confirm)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/2fa/disable",
		OperationID: "disableTwoFactor", Summary: "Turn two-factor authentication off", Tags: tags,
		Status: http.StatusNoContent, Errors: []int{http.StatusForbidden},
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.2fa_disable")}}, h.disableOwn)
}

var (
	errTwoFactorOff = endpoint.Refuse(http.StatusForbidden, "forbidden", "Two-factor authentication is disabled on this server")
	errInvalidTOTP  = endpoint.Refuse(http.StatusForbidden, "invalid_totp", "Invalid two-factor authentication code")
)

type twoFactorStatusResponse struct {
	// Enabled is true when 2FA is active (a confirmed enrollment gates login).
	Enabled bool `json:"enabled"`
	// Pending is true when an enrollment exists but has not been confirmed yet.
	Pending bool `json:"pending"`
}

// status reports whether the caller has 2FA active and/or a pending enrollment.
// GET /api/v1/auth/2fa
func (h *TwoFactorHandler) status(r *http.Request, _ struct{}) (twoFactorStatusResponse, error) {
	accountID, _, ok := twoFactorOwner(middleware.GetClaims(r))
	if !ok {
		return twoFactorStatusResponse{}, errAuthenticationRequired
	}
	tf, err := h.store.Get(accountID)
	if errors.Is(err, repositories.ErrTwoFactorNotFound) {
		return twoFactorStatusResponse{}, nil
	}
	if err != nil {
		return twoFactorStatusResponse{}, fmt.Errorf("read 2FA status: %w", err)
	}
	return twoFactorStatusResponse{Enabled: tf.ConfirmedAt != nil, Pending: tf.ConfirmedAt == nil}, nil
}

type enrollResponse struct {
	// Secret is the base32 TOTP secret, for manual entry into an authenticator.
	Secret string `json:"secret" minLength:"1"`
	// OTPAuthURL is the otpauth:// provisioning URI (QR-code payload).
	OTPAuthURL string `json:"otpauth_url" format:"uri" pattern:"^otpauth://"`
	// RecoveryCodes are the one-time recovery codes, returned ONCE at enrollment
	// (only their hashes are stored). The client must surface these to the user.
	RecoveryCodes []string `json:"recovery_codes" minItems:"1"`
}

// enroll begins 2FA enrollment: it mints a TOTP secret + recovery codes, stores
// the secret encrypted-at-rest and the recovery codes hashed, and returns the
// provisioning URI + plaintext recovery codes. The enrollment is PENDING until
// confirm verifies a first code, so a mistyped secret can never lock the user
// out. Re-enrolling replaces any prior pending enrollment.
// POST /api/v1/auth/2fa/enroll
func (h *TwoFactorHandler) enroll(r *http.Request, _ struct{}) (enrollResponse, error) {
	if !h.enabled {
		return enrollResponse{}, errTwoFactorOff
	}
	// Secrets MUST be stored encrypted at rest; refuse rather than silently
	// persist a TOTP secret in plaintext when no encryption key is configured.
	if h.masterKey == "" {
		return enrollResponse{}, endpoint.Refuse(http.StatusServiceUnavailable, "unavailable", "Two-factor authentication requires server encryption to be configured")
	}
	accountID, label, ok := twoFactorOwner(middleware.GetClaims(r))
	if !ok {
		return enrollResponse{}, errAuthenticationRequired
	}

	// A confirmed enrollment must be explicitly disabled before re-enrolling, so
	// an attacker holding a live session can't silently swap the secret out from
	// under an active 2FA user. A pending enrollment may be freely replaced.
	if existing, err := h.store.Get(accountID); err == nil && existing.ConfirmedAt != nil {
		return enrollResponse{}, endpoint.Refuse(http.StatusConflict, "conflict", "Two-factor authentication is already enabled; disable it first")
	} else if err != nil && !errors.Is(err, repositories.ErrTwoFactorNotFound) {
		return enrollResponse{}, fmt.Errorf("read 2FA status: %w", err)
	}

	key, err := auth.GenerateTOTPSecret(auth.DefaultTOTPIssuer, label)
	if err != nil {
		return enrollResponse{}, fmt.Errorf("generate 2FA secret: %w", err)
	}
	encrypted, err := crypto.EncryptString(key.Secret(), h.masterKey)
	if err != nil {
		return enrollResponse{}, fmt.Errorf("secure 2FA secret: %w", err)
	}

	recoveryCodes, err := auth.GenerateRecoveryCodes()
	if err != nil {
		return enrollResponse{}, fmt.Errorf("generate recovery codes: %w", err)
	}
	hashes := make([]string, len(recoveryCodes))
	for i, c := range recoveryCodes {
		hash, herr := hashRecoveryCode(c)
		if herr != nil {
			return enrollResponse{}, fmt.Errorf("secure recovery codes: %w", herr)
		}
		hashes[i] = hash
	}

	if _, err := h.store.Enroll(accountID, encrypted, hashes); err != nil {
		return enrollResponse{}, fmt.Errorf("save 2FA enrollment: %w", err)
	}

	return enrollResponse{
		Secret:        key.Secret(),
		OTPAuthURL:    key.URL(),
		RecoveryCodes: recoveryCodes,
	}, nil
}

// confirmRequest carries the first code from the new authenticator, in the
// field sign-in takes it in.
type confirmRequest struct {
	TOTPCode string `json:"totp_code"`
}

// confirm completes enrollment by verifying a first TOTP code against the
// pending secret, flipping the enrollment active. Until this succeeds, 2FA does
// not gate login. A wrong code is 403 invalid_totp, never 401: the session
// that sent it is still good (#392).
// POST /api/v1/auth/2fa/confirm
func (h *TwoFactorHandler) confirm(r *http.Request, req confirmRequest) (endpoint.Empty, error) {
	if !h.enabled {
		return endpoint.Empty{}, errTwoFactorOff
	}
	accountID, _, ok := twoFactorOwner(middleware.GetClaims(r))
	if !ok {
		return endpoint.Empty{}, errAuthenticationRequired
	}
	if req.TOTPCode == "" {
		return endpoint.Empty{}, endpoint.Invalid(map[string]string{"totp_code": "required"})
	}

	tf, err := h.store.Get(accountID)
	if errors.Is(err, repositories.ErrTwoFactorNotFound) {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusBadRequest, "bad_request", "No pending 2FA enrollment to confirm")
	}
	if err != nil {
		return endpoint.Empty{}, fmt.Errorf("read 2FA enrollment: %w", err)
	}
	if tf.ConfirmedAt != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusConflict, "conflict", "Two-factor authentication is already enabled")
	}

	secret, err := crypto.DecryptString(tf.EncryptedSecret, h.masterKey)
	if err != nil {
		return endpoint.Empty{}, fmt.Errorf("read 2FA secret: %w", err)
	}
	if !auth.ValidateTOTPCode(req.TOTPCode, secret) {
		return endpoint.Empty{}, errInvalidTOTP
	}
	if err := h.store.Confirm(tf.ID); err != nil {
		return endpoint.Empty{}, fmt.Errorf("enable 2FA: %w", err)
	}
	return endpoint.Empty{}, nil
}

// disableRequest is the second factor, in the fields sign-in takes it in: one
// of them, which disableOwn checks.
type disableRequest struct {
	TOTPCode     string `json:"totp_code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

// disableOwn turns 2FA off. It requires proof of possession — a valid current TOTP
// code OR an unused recovery code — so a hijacked-but-2FA-protected session
// can't strip the protection without the second factor. A wrong one is 403
// (invalid_totp, or invalid_recovery_code when only a recovery code was
// sent), never 401: the session that sent it is still good (#392).
// POST /api/v1/auth/2fa/disable
func (h *TwoFactorHandler) disableOwn(r *http.Request, req disableRequest) (endpoint.Empty, error) {
	accountID, _, ok := twoFactorOwner(middleware.GetClaims(r))
	if !ok {
		return endpoint.Empty{}, errAuthenticationRequired
	}
	if req.TOTPCode == "" && req.RecoveryCode == "" {
		return endpoint.Empty{}, endpoint.Invalid(map[string]string{"totp_code": "required unless recovery_code is sent"})
	}

	_, err := h.store.GetActive(accountID)
	if errors.Is(err, repositories.ErrTwoFactorNotFound) {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusBadRequest, "bad_request", "Two-factor authentication is not enabled")
	}
	if err != nil {
		return endpoint.Empty{}, fmt.Errorf("read 2FA enrollment: %w", err)
	}

	verified, err := h.verifySecondFactor(accountID, req.TOTPCode, req.RecoveryCode)
	if err != nil {
		return endpoint.Empty{}, fmt.Errorf("verify the second factor: %w", err)
	}
	if !verified {
		if req.TOTPCode == "" {
			return endpoint.Empty{}, endpoint.Refuse(http.StatusForbidden, "invalid_recovery_code", "Invalid or already used recovery code")
		}
		return endpoint.Empty{}, errInvalidTOTP
	}
	// Where 2FA is required (the bootstrap admin), turning it off would make the
	// requirement a one-time gate rather than a rule.
	if h.db != nil {
		var account models.Mailbox
		if err := h.db.Select("two_factor_required").First(&account, accountID).Error; err != nil {
			return endpoint.Empty{}, fmt.Errorf("read account: %w", err)
		}
		if account.TwoFactorRequired {
			return endpoint.Empty{}, endpoint.Refuse(http.StatusForbidden, "two_factor_required", "Two-factor authentication is required for this account and cannot be disabled")
		}
	}
	if err := h.disable(accountID); err != nil {
		return endpoint.Empty{}, fmt.Errorf("disable 2FA: %w", err)
	}
	h.sessions.Forget(accountID)
	return endpoint.Empty{}, nil
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
func (h *TwoFactorHandler) verifySecondFactor(accountID uint, totpCode, recoveryCode string) (bool, error) {
	return verifyTOTPOrRecovery(h.store, h.masterKey, accountID, totpCode, recoveryCode)
}

// errTwoFactorUnreadable is a TOTP code that could not be checked because the
// server cannot read the account's secrets: no encryption key is configured,
// or a stored secret does not decrypt with it.
var errTwoFactorUnreadable = errors.New("the stored two-factor secret cannot be read")

// verifyTOTPOrRecovery reports whether totpCode is a current code of one of
// the account's confirmed TOTP credentials, or recoveryCode one of its unspent
// recovery codes, which it spends. A TOTP code is tried first; a recovery code
// only when no TOTP code matched, so one is never spent needlessly.
//
// The error is why the answer could not be found: the credentials could not be
// read, a secret could not be decrypted, or a recovery code could not be
// spent. It is never a wrong code. Reporting such a failure as "invalid code"
// told a caller with the right code to try again, and charged a sign-in
// throttle for the server's own fault.
func verifyTOTPOrRecovery(store twoFactorStore, masterKey string, accountID uint, totpCode, recoveryCode string) (bool, error) {
	var unreadable error
	if totpCode != "" {
		creds, err := store.Active(accountID)
		if err != nil {
			return false, fmt.Errorf("read the authenticators: %w", err)
		}
		for i := range creds {
			if masterKey == "" {
				unreadable = errTwoFactorUnreadable
				break
			}
			secret, err := crypto.DecryptString(creds[i].EncryptedSecret, masterKey)
			if err != nil {
				// Another authenticator may still match; the failure counts
				// only if none does.
				unreadable = fmt.Errorf("%w: %w", errTwoFactorUnreadable, err)
				continue
			}
			if auth.ValidateTOTPCode(totpCode, secret) {
				return true, nil
			}
		}
	}
	if recoveryCode != "" {
		consumed, err := store.ConsumeRecoveryCode(accountID, recoveryCode)
		if err != nil {
			return false, fmt.Errorf("spend the recovery code: %w", err)
		}
		if consumed {
			return true, nil
		}
	}
	return false, unreadable
}
