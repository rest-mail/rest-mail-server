package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
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
	// RevokeAllForSubject kills every remaining active session for one owner —
	// used when a refresh detects a disabled/deleted account, so no sibling
	// session for that owner survives.
	RevokeAllForSubject(userType string, subjectID uint) error
}

// accountStateStore re-reads live account state during a refresh so a token that
// outlived a password change, disable or delete cannot be used to mint fresh
// access tokens. It also lists an admin's capabilities, from its grants, for the
// response. A nil store (no database, some unit tests) skips the re-check.
type accountStateStore interface {
	// MailboxActive reports whether the mailbox exists and is active.
	MailboxActive(id uint) (bool, error)
	// AdminState reports what a refresh needs to know about an admin now.
	AdminState(id uint) (adminState, error)
}

// dbAccountState is the gorm-backed accountStateStore used in production.
type dbAccountState struct {
	db *gorm.DB
}

func (s dbAccountState) MailboxActive(id uint) (bool, error) {
	var mb models.Mailbox
	err := s.db.Select("id", "active").First(&mb, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return mb.Active, nil
}

// adminState is an admin account as a refresh must see it: whether it exists
// and is active, the capabilities it holds at "*" (for the client to show; they
// authorise nothing), whether it is still in setup (see adminSetupSteps), and
// its current credential epoch.
type adminState struct {
	Active          bool
	Capabilities    []string
	SetupPending    bool
	CredentialEpoch int
}

func (s dbAccountState) AdminState(id uint) (adminState, error) {
	repo := repositories.NewAdminUserRepository(s.db)
	user, err := repo.GetByID(id)
	if errors.Is(err, repositories.ErrUserNotFound) {
		return adminState{}, nil
	}
	if err != nil {
		return adminState{}, err
	}
	// An expired bootstrap password ends the sessions it opened, too.
	if !user.Active || user.BootstrapExpired(time.Now()) {
		return adminState{}, nil
	}
	held, err := grants.Load(s.db, models.GrantHolderAdmin, id, time.Now())
	if err != nil {
		return adminState{}, err
	}
	steps, err := adminSetupSteps(repositories.NewTwoFactorRepository(s.db), user)
	if err != nil {
		return adminState{}, err
	}
	return adminState{Active: true, Capabilities: held.Capabilities(), SetupPending: len(steps) > 0, CredentialEpoch: user.CredentialEpoch}, nil
}

// Setup steps an admin account may still owe before it can use its roles.
const (
	setupStepPassword  = "password"
	setupStepTwoFactor = "two_factor"
)

// adminSetupSteps lists what the account must still do before its sessions
// carry its capabilities: replace a password someone else chose, and enroll a
// TOTP authenticator where 2FA is required. Empty means the account is set up.
func adminSetupSteps(store twoFactorStore, user *models.AdminUser) ([]string, error) {
	var steps []string
	if user.PasswordChangeRequired {
		steps = append(steps, setupStepPassword)
	}
	if user.TwoFactorRequired {
		if store == nil {
			// No way to check an enrollment: fail closed, the account stays in setup.
			return append(steps, setupStepTwoFactor), nil
		}
		_, err := store.GetActive(models.TwoFactorUserTypeAdmin, user.ID)
		if errors.Is(err, repositories.ErrTwoFactorNotFound) {
			steps = append(steps, setupStepTwoFactor)
		} else if err != nil {
			return nil, err
		}
	}
	return steps, nil
}

type AuthHandler struct {
	db           *gorm.DB
	jwtService   *auth.JWTService
	refreshStore refreshTokenStore
	// accountState re-derives live account state (active flag, admin
	// capabilities) on refresh. Nil in DB-less unit tests, which then trust the
	// refresh-token claims unchanged.
	accountState accountStateStore
	// twoFactorStore + masterKey back the optional TOTP 2FA enforcement in the
	// login path (OSI-19). When an account has 2FA active, a correct password is
	// necessary but not sufficient — a valid current code (or recovery code) is
	// also required before tokens are issued. masterKey decrypts the stored
	// per-account secret. A nil store (no DB, some unit tests) disables the check.
	twoFactorStore twoFactorStore
	masterKey      string
	// pushDevices ends the push devices of a session a logout ends (#363).
	// Nil when push is off.
	pushDevices sessionDevices
}

// sessionDevices is what a logout needs of the push device store: to delete
// the devices of a session once it has ended.
type sessionDevices interface {
	SessionEnded(sid string, now time.Time) (int, error)
}

// SetPushDevices makes a logout delete the push devices of the session it
// ends. Call before the router serves traffic.
func (h *AuthHandler) SetPushDevices(devices sessionDevices) {
	h.pushDevices = devices
}

// NewAuthHandler wires the auth handler. masterKey is the MASTER_KEY used to
// decrypt stored TOTP 2FA secrets during login enforcement; pass "" when
// encryption is not configured (2FA enrollment is then refused elsewhere, and
// login enforcement degrades to recovery-code-only for any pre-existing
// enrollment — a secret that could not have been stored anyway).
func NewAuthHandler(db *gorm.DB, jwtService *auth.JWTService, masterKey string) *AuthHandler {
	var refreshStore refreshTokenStore
	var tfStore twoFactorStore
	var accountState accountStateStore
	if db != nil {
		refreshStore = repositories.NewRefreshTokenRepository(db)
		tfStore = repositories.NewTwoFactorRepository(db)
		accountState = dbAccountState{db: db}
	}
	return &AuthHandler{db: db, jwtService: jwtService, refreshStore: refreshStore, accountState: accountState, twoFactorStore: tfStore, masterKey: masterKey}
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
		SessionID: tokens.SessionID,
		Status:    models.RefreshTokenActive,
		ExpiresAt: tokens.RefreshExpiresAt,
	})
}

// revokeAllForSubject best-effort revokes every remaining active session for one
// owner. Used from the refresh path when a disabled/deleted account is detected;
// failures are non-fatal (the refresh is refused regardless) but logged.
func (h *AuthHandler) revokeAllForSubject(userType string, subjectID uint) {
	if h.refreshStore == nil {
		return
	}
	if err := h.refreshStore.RevokeAllForSubject(userType, subjectID); err != nil {
		slog.Warn("failed to revoke sessions on refresh", "user_type", userType, "subject_id", subjectID, "error", err)
	}
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
	// Client is "native" for an app that is not a browser (the mobile client):
	// it gets the tokens in the response body instead of as cookies, and keeps
	// them in its own secure storage. Empty means a browser. Anything else is
	// refused, so a typo cannot quietly fall back to cookies.
	Client string `json:"client,omitempty"`
}

// clientNative is the loginRequest.Client value that asks for body tokens.
const clientNative = "native"

// refreshRequest is the body a native client sends to /auth/refresh and
// /auth/logout. Browsers send no body; their refresh token is the cookie.
type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// loginResponse is the login/refresh success body. For a browser it carries NO
// tokens: they are delivered ONLY as httpOnly cookies (see deliverSession), so
// page JavaScript — and therefore any XSS — can neither read them at rest nor
// scrape them from this response. ExpiresIn lets the SPA schedule a pre-emptive
// refresh without ever seeing the token; User and Capabilities let it restore
// session UI state on boot from /auth/refresh. Capabilities are what an admin
// holds at "*", from its grants, for the client to show what it can do; they
// authorise nothing (the admin API reads the grants on every request).
//
// The token fields are filled only for a native client: one that logged in with
// "client": "native", or refreshed by sending its refresh token in the body. A
// request that arrives with the refresh cookie is a browser's and never gets
// them, whatever its body says.
type loginResponse struct {
	ExpiresIn    int      `json:"expires_in"`
	User         userInfo `json:"user"`
	Capabilities []string `json:"capabilities,omitempty"` // For admin users
	// SetupRequired lists what the account must still do — "password" (POST
	// /api/v1/auth/password), "two_factor" (POST /api/v1/auth/2fa/enroll, then
	// /confirm) — before the session can use the admin API, which answers 403
	// setup_required until then. Absent when the account is set up.
	SetupRequired []string `json:"setup_required,omitempty"`

	AccessToken      string `json:"access_token,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	RefreshExpiresIn int    `json:"refresh_expires_in,omitempty"`
}

// deliverSession hands a freshly minted token pair to the client. A browser gets
// the httpOnly refresh cookie plus the access and CSRF cookies, and resp is left
// token-free. A native client gets the tokens in resp and no cookies at all, so
// it never depends on cookie names or paths.
func deliverSession(w http.ResponseWriter, tokens *auth.TokenPair, native bool, resp *loginResponse) error {
	if native {
		resp.AccessToken = tokens.AccessToken
		resp.RefreshToken = tokens.RefreshToken
		resp.RefreshExpiresIn = int(time.Until(tokens.RefreshExpiresAt).Seconds())
		return nil
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.RefreshCookieName,
		Value:    tokens.RefreshToken,
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 60 * 60,
	})
	return setSessionCookies(w, tokens.AccessToken, tokens.ExpiresIn)
}

// presentedRefreshToken returns the refresh token a request carries, and
// whether it came in the body (a native client). The cookie wins: a request
// that carries restmail_refresh is a browser's, and is answered with cookies
// only, so script running in a page can never trade the httpOnly cookie for a
// readable token by adding a body.
func presentedRefreshToken(r *http.Request) (token string, native bool) {
	if c, err := r.Cookie(auth.RefreshCookieName); err == nil && c.Value != "" {
		return c.Value, false
	}
	var body refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body.RefreshToken != "" {
		return body.RefreshToken, true
	}
	return "", false
}

// setSessionCookies issues the browser session cookies for a freshly minted
// access token: the httpOnly, Secure, SameSite=Strict restmail_access cookie
// (the ONLY place the access token is handed to a browser) and its non-httpOnly
// restmail_csrf companion for the double-submit CSRF defence. Both are scoped to
// Path=/ so they accompany every API and SSE request, and both expire with the
// access token (the SPA refreshes before then, which rotates them). A fresh
// random CSRF token is minted per call.
//
// Secure is set unconditionally: the stack is served over HTTPS (and browsers
// treat the *.localhost testbed as a secure context), matching the pre-existing
// restmail_refresh cookie. If a plain-HTTP deployment is ever required, gate
// this one flag behind cfg.IsProduction rather than defaulting it off.
func setSessionCookies(w http.ResponseWriter, accessToken string, accessMaxAgeSecs int) error {
	csrfToken, err := newCSRFToken()
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.AccessCookieName,
		Value:    accessToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   accessMaxAgeSecs,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CSRFCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: false, // MUST be readable so the SPA can echo it in X-CSRF-Token.
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   accessMaxAgeSecs,
	})
	return nil
}

// clearSessionCookies expires the access and CSRF cookies (logout). The refresh
// cookie is cleared separately by Logout because it is scoped to a different
// path.
func clearSessionCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: auth.AccessCookieName, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name: auth.CSRFCookieName, Value: "", Path: "/",
		HttpOnly: false, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
}

// newCSRFToken returns a 128-bit cryptographically random, hex-encoded CSRF
// token — the value placed in the restmail_csrf cookie and required back in the
// X-CSRF-Token header.
func newCSRFToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate CSRF token: %w", err)
	}
	return hex.EncodeToString(b), nil
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

	if req.Client != "" && req.Client != clientNative {
		respond.ValidationError(w, map[string]string{
			"client": `must be "native" or omitted`,
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
// supplied), and whether a second factor was verified. On failure it writes the
// response and returns false — with a distinct totp_required code when no
// second factor was supplied, so a client knows to prompt for one.
func (h *AuthHandler) enforce2FA(w http.ResponseWriter, userType string, subjectID uint, req loginRequest) (proceed, verified bool) {
	if h.twoFactorStore == nil {
		return true, false
	}
	tf, err := h.twoFactorStore.GetActive(userType, subjectID)
	if errors.Is(err, repositories.ErrTwoFactorNotFound) {
		return true, false // account has no active 2FA — unchanged behaviour
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to verify two-factor authentication")
		return false, false
	}
	if verifyTOTPOrRecovery(h.twoFactorStore, h.masterKey, tf, req.TOTPCode, req.RecoveryCode) {
		return true, true
	}
	if req.TOTPCode == "" && req.RecoveryCode == "" {
		respond.Error(w, http.StatusUnauthorized, "totp_required", "Two-factor authentication code required")
	} else {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid two-factor authentication code")
	}
	return false, false
}

// signInMethods is the amr of a session that signed in with a password, and a
// second factor if verified: a TOTP code or a recovery code.
func signInMethods(verified bool) []string {
	if verified {
		return []string{auth.AMRPassword, auth.AMROTP}
	}
	return []string{auth.AMRPassword}
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
	if adminUser.BootstrapExpired(time.Now()) {
		respond.Error(w, http.StatusUnauthorized, "bootstrap_expired",
			"This server's bootstrap password has expired. Deploy it again with a new one, then sign in with that.")
		return
	}

	// Second factor (OSI-19): only reached with a correct password, so a 2FA
	// challenge never leaks account existence. No-op for admins without 2FA.
	ok, verified := h.enforce2FA(w, "admin", adminUser.ID, req)
	if !ok {
		return
	}

	h.startAdminSession(w, adminUser, req.Client == clientNative, signInMethods(verified))
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
	ok, twoFactor := h.enforce2FA(w, "mailbox", mailbox.ID, req)
	if !ok {
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

	// Generate tokens. The session remembers whether the sign-in took a second
	// factor: creating an app password needs one, on a mailbox that has 2FA.
	tokens, err := h.jwtService.GenerateMailboxSession(mailbox.ID, mailbox.Address, account.ID, twoFactor)
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

	resp := loginResponse{
		ExpiresIn: tokens.ExpiresIn,
		User: userInfo{
			ID:          account.ID,
			Email:       mailbox.Address,
			DisplayName: mailbox.DisplayName,
		},
	}
	if err := deliverSession(w, tokens, req.Client == clientNative, &resp); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to establish session")
		return
	}

	// Update last login
	h.db.Model(&mailbox).Update("last_login_at", time.Now())

	respond.Data(w, http.StatusOK, resp)
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	// Revoke the presented refresh token server-side so it can no longer be
	// exchanged (OSI-10: logout was previously client-side only). The token is
	// the cookie, or for a native client the body. Best-effort: a missing or
	// invalid token still clears client state below. Idempotent, so a double
	// logout is harmless.
	if token, _ := presentedRefreshToken(r); token != "" && h.refreshStore != nil {
		if claims, err := h.jwtService.ValidateRefreshToken(token); err == nil && claims.ID != "" {
			_ = h.refreshStore.Revoke(claims.ID)
			// The phones the session registered for push go with it.
			if h.pushDevices != nil {
				if _, err := h.pushDevices.SessionEnded(claims.SessionID, time.Now()); err != nil {
					slog.Warn("failed to remove the push devices of a session that signed out", "error", err)
				}
			}
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.RefreshCookieName,
		Value:    "",
		Path:     "/api/v1/auth",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	// Also expire the access + CSRF session cookies so the browser holds no
	// usable session material after logout.
	clearSessionCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	token, native := presentedRefreshToken(r)
	if token == "" {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "No refresh token")
		return
	}

	claims, err := h.jwtService.ValidateRefreshToken(token)
	if err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired refresh token")
		return
	}

	// Re-read live account state BEFORE consuming the rotation, so a token that
	// outlived a password change, disable or delete cannot mint a fresh access
	// token, and the capabilities the response lists are the grants' now. A nil
	// store (no DB, some unit tests) trusts the claims unchanged.
	//
	// Like the rotation-ledger check below, this path fails CLOSED: an
	// unreachable store refuses the refresh (401) rather than minting tokens
	// without re-verifying account state.
	//
	// capabilities is what the response lists: what the grants give at "*"
	// when the account was re-read, otherwise nothing.
	var capabilities []string
	// A session whose account is in setup stays that way until the setup is
	// done: the refresh must not be a way out of the restriction.
	setupPending := claims.SetupPending
	epoch := claims.CredentialEpoch
	if h.accountState != nil {
		if claims.UserType == "admin" {
			state, stateErr := h.accountState.AdminState(claims.AdminUserID)
			active, dbCaps, inSetup := state.Active, state.Capabilities, state.SetupPending
			epoch = state.CredentialEpoch
			if stateErr != nil {
				respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired refresh token")
				return
			}
			if !active {
				h.revokeAllForSubject("admin", claims.AdminUserID)
				respond.Error(w, http.StatusUnauthorized, "unauthorized", "Account is disabled")
				return
			}
			capabilities = dbCaps
			setupPending = inSetup
		} else {
			active, stateErr := h.accountState.MailboxActive(claims.MailboxID)
			if stateErr != nil {
				respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired refresh token")
				return
			}
			if !active {
				h.revokeAllForSubject("mailbox", claims.MailboxID)
				respond.Error(w, http.StatusUnauthorized, "unauthorized", "Account is disabled")
				return
			}
		}
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
	//
	// The new tokens stay in the session: the same sid.
	session := auth.ContinueSession(claims.SessionID)
	var tokens *auth.TokenPair
	var userType string
	var subjectID uint
	if claims.UserType == "admin" && setupPending {
		capabilities = nil
		tokens, err = h.jwtService.GenerateSetupTokenPair(claims.AdminUserID, claims.Username, epoch, claims.AMR, session)
		userType, subjectID = "admin", claims.AdminUserID
	} else if claims.UserType == "admin" {
		// A refresh keeps the session's sign-in methods: finishing 2FA setup
		// and refreshing is not signing in with 2FA.
		tokens, err = h.jwtService.GenerateAdminSession(claims.AdminUserID, claims.Username, epoch, claims.AMR, session)
		userType, subjectID = "admin", claims.AdminUserID
	} else {
		tokens, err = h.jwtService.GenerateMailboxSession(claims.MailboxID, claims.Email, claims.WebmailAccountID, claims.TwoFactor, session)
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

	// For a browser the body carries only what the SPA needs to restore session
	// UI on boot (identity + capabilities + expiry) and the tokens go out as
	// cookies; a native client gets them in the body, on the same channel its
	// refresh token came in on. User identity is taken from the validated
	// refresh-token claims, so this stays a single DB-touch-free response.
	resp := loginResponse{ExpiresIn: tokens.ExpiresIn}
	if claims.UserType == "admin" {
		resp.Capabilities = capabilities
		resp.User = userInfo{ID: claims.AdminUserID, Email: claims.Username, DisplayName: claims.Username}
	} else {
		resp.User = userInfo{ID: claims.WebmailAccountID, Email: claims.Email, DisplayName: claims.Email}
	}
	if err := deliverSession(w, tokens, native, &resp); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to establish session")
		return
	}
	respond.Data(w, http.StatusOK, resp)
}

// minChosenPasswordLength is bootstrap's, so every way of choosing an admin
// password asks the same of it.
const minChosenPasswordLength = bootstrap.MinChosenPasswordLength

// changePasswordRequest is the body of POST /api/v1/auth/password.
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword lets a signed-in admin replace their own password, and is the
// one thing a session flagged password_change_required may do. It needs the
// current password even then, so a stolen access token alone cannot take the
// account. Success clears the flag and revokes every session of the account,
// including this one: the next sign-in gets the account's real capabilities.
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if claims.UserType != "admin" {
		respond.Error(w, http.StatusForbidden, "forbidden", "Only admin accounts change their password here")
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		respond.ValidationError(w, map[string]string{
			"current_password": "required",
			"new_password":     "required",
		})
		return
	}

	repo := repositories.NewAdminUserRepository(h.db)
	user, err := repo.GetByID(claims.AdminUserID)
	if err != nil || !user.Active {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired session")
		return
	}
	if auth.CheckPassword(req.CurrentPassword, user.PasswordHash) != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Current password is incorrect")
		return
	}
	if len(req.NewPassword) < minChosenPasswordLength {
		respond.ValidationError(w, map[string]string{
			"new_password": fmt.Sprintf("must be at least %d characters", minChosenPasswordLength),
		})
		return
	}
	// The point of a forced change is a credential nobody else has seen.
	if req.NewPassword == req.CurrentPassword {
		respond.ValidationError(w, map[string]string{"new_password": "must differ from the current password"})
		return
	}

	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to set password")
		return
	}
	if err := h.db.Model(&models.AdminUser{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
		"password_hash":            hash,
		"last_password_change":     time.Now(),
		"password_change_required": false,
		// Its own password now: the deploy-time one, and its deadline, are gone.
		"bootstrap_expires_at": nil,
		// Every token of the account is dead from now, this one included.
		"credential_epoch": gorm.Expr("credential_epoch + 1"),
	}).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to set password")
		return
	}

	h.revokeAllForSubject("admin", user.ID)
	respond.Data(w, http.StatusOK, map[string]string{"message": "Password changed. Sign in again with the new password."})
}

// startAdminSession answers with a fresh session for an admin whose identity is
// already proven — by password and second factor at login, or by a single-use
// token at reset — with amr saying how. An account still in setup gets a
// session that can only finish the setup (see GenerateSetupTokenPair).
func (h *AuthHandler) startAdminSession(w http.ResponseWriter, adminUser *models.AdminUser, native bool, amr []string) {
	// What the grants give at "*", for the client to show.
	held, err := grants.Load(h.db, models.GrantHolderAdmin, adminUser.ID, time.Now())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to load user capabilities")
		return
	}
	capNames := held.Capabilities()

	setupSteps, err := adminSetupSteps(h.twoFactorStore, adminUser)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to read account state")
		return
	}
	var tokens *auth.TokenPair
	if len(setupSteps) > 0 {
		capNames = nil
		tokens, err = h.jwtService.GenerateSetupTokenPair(adminUser.ID, adminUser.Username, adminUser.CredentialEpoch, amr)
	} else {
		tokens, err = h.jwtService.GenerateAdminSession(adminUser.ID, adminUser.Username, adminUser.CredentialEpoch, amr)
	}
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

	resp := loginResponse{
		ExpiresIn:     tokens.ExpiresIn,
		Capabilities:  capNames,
		SetupRequired: setupSteps,
		User: userInfo{
			ID:          adminUser.ID,
			Email:       adminUser.Username, // Use username in email field for compatibility
			DisplayName: adminUser.Username,
		},
	}
	if err := deliverSession(w, tokens, native, &resp); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to establish session")
		return
	}

	// Update last login
	h.db.Model(adminUser).Update("updated_at", time.Now())

	respond.Data(w, http.StatusOK, resp)
}

// resetRequest is the body of POST /api/v1/auth/reset.
type resetRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
	Client   string `json:"client,omitempty"`
}

// Reset redeems a reset_admin token — minted by `restmail-api mint-token
// --purpose reset-admin`. The admin it names gets the password the redeemer
// chose, loses its authenticator and must enroll a new one, and every session it
// had ends: whoever held the old credentials, a person or an agent that set the
// server up, is locked out.
func (h *AuthHandler) Reset(w http.ResponseWriter, r *http.Request) {
	var req resetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Client != "" && req.Client != clientNative {
		respond.ValidationError(w, map[string]string{"client": `must be "native" or omitted`})
		return
	}
	admin, err := bootstrap.ResetAdmin(h.db, req.Token, req.Password)
	if h.redeemFailed(w, err) {
		return
	}
	// Redeeming a token is neither a password nor a second factor: the
	// session records neither, and is in setup until a new authenticator is
	// enrolled anyway.
	h.startAdminSession(w, admin, req.Client == clientNative, nil)
}

// redeemFailed answers for a token that could not be redeemed, and says
// whether it did.
func (h *AuthHandler) redeemFailed(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, bootstrap.ErrTokenInvalid):
		respond.Error(w, http.StatusUnauthorized, "invalid_token", err.Error())
	case errors.Is(err, bootstrap.ErrPasswordTooShort):
		respond.ValidationError(w, map[string]string{"password": err.Error()})
	default:
		slog.Error("redeeming a one-time token failed", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to redeem the token")
	}
	return true
}

// AdminSessionCurrent is the database's answer to whether an admin access token
// is still current (middleware.RejectStaleAdminSessions): the account must exist
// and be active, not be a bootstrap admin whose deploy-time password has
// expired, and the token must carry its current credential epoch — a reset or a
// password change bumps it.
func AdminSessionCurrent(db *gorm.DB) middleware.AdminSessionCurrent {
	return func(adminID uint, epoch int) (bool, error) {
		var admin models.AdminUser
		err := db.Select("active", "credential_epoch", "password_change_required", "bootstrap_expires_at").
			First(&admin, adminID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !admin.Active || admin.BootstrapExpired(time.Now()) {
			return false, nil
		}
		return admin.CredentialEpoch == epoch, nil
	}
}
