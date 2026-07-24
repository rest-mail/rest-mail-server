package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// refreshTokenStore is the rotation/revocation ledger the auth handler needs.
// A consumer-side interface (implemented by repositories.RefreshTokenRepository)
// so the handler's rotation/revocation state machine can be unit-tested against
// an in-memory fake without a database.
type refreshTokenStore interface {
	Save(rec *models.RefreshToken) error
	Rotate(jti string) error
	Revoke(jti string) error
}

type AuthHandler struct {
	db           *gorm.DB
	jwtService   *auth.JWTService
	refreshStore refreshTokenStore
	// twoFactorStore + masterKey back the optional TOTP 2FA enforcement in the
	// login path (OSI-19). When an account has 2FA active, a correct password is
	// necessary but not sufficient — a valid current code (or recovery code) is
	// also required before tokens are issued. masterKey decrypts the stored
	// per-account secret. A nil store (no DB, some unit tests) disables the check.
	twoFactorStore twoFactorStore
	masterKey      string
}

// NewAuthHandler wires the auth handler. masterKey is the MASTER_KEY used to
// decrypt stored TOTP 2FA secrets during login enforcement; pass "" when
// encryption is not configured (2FA enrollment is then refused elsewhere, and
// login enforcement degrades to recovery-code-only for any pre-existing
// enrollment — a secret that could not have been stored anyway).
func NewAuthHandler(db *gorm.DB, jwtService *auth.JWTService, masterKey string) *AuthHandler {
	var refreshStore refreshTokenStore
	var tfStore twoFactorStore
	if db != nil {
		refreshStore = repositories.NewRefreshTokenRepository(db)
		tfStore = repositories.NewTwoFactorRepository(db)
	}
	return &AuthHandler{db: db, jwtService: jwtService, refreshStore: refreshStore, twoFactorStore: tfStore, masterKey: masterKey}
}

// persistRefreshToken records a freshly issued refresh token in the rotation
// ledger (status active). A nil store (no database, e.g. some unit tests) is a
// no-op — production always wires the DB-backed store via NewAuthHandler.
func (h *AuthHandler) persistRefreshToken(tokens *auth.TokenPair, userType string, subjectID uint) error {
	if h.refreshStore == nil {
		return nil
	}
	return h.refreshStore.Save(&models.RefreshToken{
		Jti:       tokens.RefreshJTI,
		UserType:  userType,
		SubjectID: subjectID,
		Status:    models.RefreshTokenActive,
		ExpiresAt: tokens.RefreshExpiresAt,
	})
}

type loginRequest struct {
	Email    string `json:"email,omitempty"`    // For mailbox users
	Username string `json:"username,omitempty"` // For admin users
	Password string `json:"password"`
	// TOTPCode / RecoveryCode carry the second factor for accounts with 2FA
	// active (OSI-19). Both are optional and ignored for accounts without 2FA,
	// so non-2FA logins are byte-for-byte unchanged. A single-step login that
	// omits them for a 2FA account is answered with a totp_required challenge.
	TOTPCode     string `json:"totp_code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

type loginResponse struct {
	AccessToken  string   `json:"access_token"`
	ExpiresIn    int      `json:"expires_in"`
	User         userInfo `json:"user"`
	Capabilities []string `json:"capabilities,omitempty"` // For admin users
}

type userInfo struct {
	ID          uint   `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Validate that either email or username is provided
	if (req.Email == "" && req.Username == "") || req.Password == "" {
		respond.ValidationError(w, map[string]string{
			"email/username": "either email or username is required",
			"password":       "required",
		})
		return
	}

	// Admin user login path
	if req.Username != "" {
		h.loginAdmin(w, req)
		return
	}

	// Mailbox user login path
	h.loginMailbox(w, req)
}

// enforce2FA gates token issuance on the second factor when an account has TOTP
// 2FA active. It MUST be called only after the password has been verified: the
// enrollment lookup happens here, so whether 2FA is enabled never leaks to a
// caller who has not already proven the password (OSI-19 + OSI-24). Returns
// true when login may proceed (no active 2FA, or a valid TOTP/recovery code was
// supplied). On failure it writes the response and returns false — with a
// distinct totp_required code when no second factor was supplied, so a client
// knows to prompt for one.
func (h *AuthHandler) enforce2FA(w http.ResponseWriter, userType string, subjectID uint, req loginRequest) bool {
	if h.twoFactorStore == nil {
		return true
	}
	tf, err := h.twoFactorStore.GetActive(userType, subjectID)
	if errors.Is(err, repositories.ErrTwoFactorNotFound) {
		return true // account has no active 2FA — unchanged behaviour
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to verify two-factor authentication")
		return false
	}
	if verifyTOTPOrRecovery(h.twoFactorStore, h.masterKey, tf, req.TOTPCode, req.RecoveryCode) {
		return true
	}
	if req.TOTPCode == "" && req.RecoveryCode == "" {
		respond.Error(w, http.StatusUnauthorized, "totp_required", "Two-factor authentication code required")
	} else {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid two-factor authentication code")
	}
	return false
}

func (h *AuthHandler) loginAdmin(w http.ResponseWriter, req loginRequest) {
	username, password := req.Username, req.Password
	adminUserRepo := repositories.NewAdminUserRepository(h.db)

	// Find the admin user. A miss does NOT short-circuit: we still run one bcrypt
	// comparison (against a dummy hash) so an unknown username costs the same as a
	// wrong password and the two are indistinguishable by timing or message
	// (OSI-24 user-enumeration defense-in-depth).
	adminUser, lookupErr := adminUserRepo.GetByUsername(username)
	passwordHash := auth.DummyPasswordHash
	if lookupErr == nil {
		passwordHash = adminUser.PasswordHash
	}
	pwErr := auth.CheckPassword(password, passwordHash)
	if lookupErr != nil || pwErr != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid username or password")
		return
	}

	// Credentials verified. Account-state checks (which legitimately surface a
	// distinct message) run only after a correct password, so they never leak
	// account existence to an unauthenticated guesser.
	if !adminUser.Active {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Account is disabled")
		return
	}

	// Second factor (OSI-19): only reached with a correct password, so a 2FA
	// challenge never leaks account existence. No-op for admins without 2FA.
	if !h.enforce2FA(w, "admin", adminUser.ID, req) {
		return
	}

	// Get capabilities
	capabilities, err := adminUserRepo.GetCapabilities(adminUser.ID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to load user capabilities")
		return
	}

	// Convert capabilities to string slice
	capNames := make([]string, len(capabilities))
	for i, cap := range capabilities {
		capNames[i] = cap.Name
	}

	// Generate admin tokens
	tokens, err := h.jwtService.GenerateAdminTokenPair(adminUser.ID, adminUser.Username, capNames)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to generate tokens")
		return
	}

	// Record the refresh token in the rotation ledger before handing it out, so
	// it can later be rotated on use and revoked on logout (OSI-10).
	if err := h.persistRefreshToken(tokens, "admin", adminUser.ID); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to persist session")
		return
	}

	// Set refresh token as HTTP-only cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "restmail_refresh",
		Value:    tokens.RefreshToken,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 60 * 60,
	})

	// Update last login
	h.db.Model(adminUser).Update("updated_at", time.Now())

	respond.Data(w, http.StatusOK, loginResponse{
		AccessToken:  tokens.AccessToken,
		ExpiresIn:    tokens.ExpiresIn,
		Capabilities: capNames,
		User: userInfo{
			ID:          adminUser.ID,
			Email:       adminUser.Username, // Use username in email field for compatibility
			DisplayName: adminUser.Username,
		},
	})
}

func (h *AuthHandler) loginMailbox(w http.ResponseWriter, req loginRequest) {
	email, password := req.Email, req.Password
	// Find the mailbox. As with admin login, a miss does NOT short-circuit: run
	// one bcrypt comparison (against a dummy hash) so an unknown or inactive
	// address is timing- and message-indistinguishable from a wrong password
	// (OSI-24 user-enumeration defense-in-depth).
	var mailbox models.Mailbox
	lookupErr := h.db.Where("address = ? AND active = ?", email, true).First(&mailbox).Error
	passwordHash := auth.DummyPasswordHash
	if lookupErr == nil {
		passwordHash = mailbox.Password
	}
	pwErr := auth.CheckPassword(password, passwordHash)
	if lookupErr != nil || pwErr != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid email or password")
		return
	}

	// Second factor (OSI-19): only reached with a correct password, so a 2FA
	// challenge never leaks whether the address exists or has 2FA. No-op for
	// mailboxes without 2FA, before any account side effects.
	if !h.enforce2FA(w, "mailbox", mailbox.ID, req) {
		return
	}

	// Find or create webmail account
	var account models.WebmailAccount
	if err := h.db.Where("primary_mailbox_id = ?", mailbox.ID).First(&account).Error; err != nil {
		account = models.WebmailAccount{PrimaryMailboxID: mailbox.ID}
		if err := h.db.Create(&account).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create account")
			return
		}
	}

	// Generate tokens
	tokens, err := h.jwtService.GenerateTokenPair(mailbox.ID, mailbox.Address, account.ID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to generate tokens")
		return
	}

	// Record the refresh token in the rotation ledger before handing it out
	// (OSI-10). Keyed by mailbox ID so a password change / account disable can
	// revoke every session for that mailbox.
	if err := h.persistRefreshToken(tokens, "mailbox", mailbox.ID); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to persist session")
		return
	}

	// Set refresh token as HTTP-only cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "restmail_refresh",
		Value:    tokens.RefreshToken,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 60 * 60,
	})

	// Update last login
	h.db.Model(&mailbox).Update("last_login_at", time.Now())

	respond.Data(w, http.StatusOK, loginResponse{
		AccessToken: tokens.AccessToken,
		ExpiresIn:   tokens.ExpiresIn,
		User: userInfo{
			ID:          account.ID,
			Email:       mailbox.Address,
			DisplayName: mailbox.DisplayName,
		},
	})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	// Revoke the presented refresh token server-side so it can no longer be
	// exchanged (OSI-10: logout was previously client-side only). Best-effort:
	// a missing/invalid cookie still clears client state below. Idempotent, so a
	// double logout is harmless.
	if cookie, err := r.Cookie("restmail_refresh"); err == nil && h.refreshStore != nil {
		if claims, err := h.jwtService.ValidateRefreshToken(cookie.Value); err == nil && claims.ID != "" {
			_ = h.refreshStore.Revoke(claims.ID)
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "restmail_refresh",
		Value:    "",
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("restmail_refresh")
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "No refresh token")
		return
	}

	claims, err := h.jwtService.ValidateRefreshToken(cookie.Value)
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired refresh token")
		return
	}

	// Rotation + revocation (OSI-10): the presented refresh token must still be
	// the ACTIVE ledger row for its jti. Rotate flips active→rotated atomically;
	// it fails (not-found) when the row is missing, already rotated (reuse of a
	// spent token), or revoked (logout / password change). Any of those refuses
	// the refresh. A nil store (no DB, some unit tests) skips this check.
	if h.refreshStore != nil {
		if err := h.refreshStore.Rotate(claims.ID); err != nil {
			respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired refresh token")
			return
		}
	}

	// Reissue the SAME kind of token the refresh token represents. Previously
	// this always used the mailbox generator, so refreshing an admin session
	// produced a mailbox access token (UserType="mailbox", no capabilities,
	// MailboxID=0) — every admin route then 403'd and the admin was locked out
	// until a full re-login.
	var tokens *auth.TokenPair
	var userType string
	var subjectID uint
	if claims.UserType == "admin" {
		tokens, err = h.jwtService.GenerateAdminTokenPair(claims.AdminUserID, claims.Username, claims.Capabilities)
		userType, subjectID = "admin", claims.AdminUserID
	} else {
		tokens, err = h.jwtService.GenerateTokenPair(claims.MailboxID, claims.Email, claims.WebmailAccountID)
		userType, subjectID = "mailbox", claims.MailboxID
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to generate tokens")
		return
	}

	// Record the replacement refresh token as the new active ledger row.
	if err := h.persistRefreshToken(tokens, userType, subjectID); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to persist session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "restmail_refresh",
		Value:    tokens.RefreshToken,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 60 * 60,
	})

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"access_token": tokens.AccessToken,
		"expires_in":   tokens.ExpiresIn,
	})
}
