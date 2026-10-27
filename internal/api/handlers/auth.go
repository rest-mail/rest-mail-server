package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	oapifly "github.com/antimatter-studios/go-oapifly"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	apitypes "github.com/restmail/restmail/internal/api/types"
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
	// RevokeAllForAccount kills every remaining active session of one account —
	// used when a refresh detects a disabled/deleted account, so no sibling
	// session of that account survives.
	RevokeAllForAccount(accountID uint) error
}

// accountStateStore re-reads live account state during a refresh so a token that
// outlived a password change, disable or delete cannot be used to mint fresh
// access tokens. It also lists the account's capabilities, from its grants, for
// the response. A nil store (no database, some unit tests) skips the re-check.
type accountStateStore interface {
	// AccountState reports what a refresh needs to know about an account now.
	AccountState(id uint) (accountState, error)
}

// dbAccountState is the gorm-backed accountStateStore used in production.
type dbAccountState struct {
	db *gorm.DB
}

// accountState is an account as a refresh must see it: whether it exists and
// is active, its address, the capabilities it holds at "*" (for the client to
// show; they authorise nothing), whether it is still in setup (see
// accountSetupSteps), and its current credential epoch.
type accountState struct {
	Active             bool
	Address            string
	DisplayName        string
	Capabilities       []string
	ScopedCapabilities []grants.Entry
	SetupPending       bool
	SetupRequired      []string
	CredentialEpoch    int
}

func (s dbAccountState) AccountState(id uint) (accountState, error) {
	var account models.Mailbox
	err := s.db.First(&account, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return accountState{}, nil
	}
	if err != nil {
		return accountState{}, err
	}
	// An expired bootstrap password ends the sessions it opened, too.
	if !account.Active || account.BootstrapExpired(time.Now()) {
		return accountState{}, nil
	}
	held, err := grants.Load(s.db, id, time.Now())
	if err != nil {
		return accountState{}, err
	}
	steps, err := accountSetupSteps(repositories.NewTwoFactorRepository(s.db), &account)
	if err != nil {
		return accountState{}, err
	}
	return accountState{
		Active:             true,
		Address:            account.Address,
		DisplayName:        account.DisplayName,
		Capabilities:       held.Capabilities(),
		ScopedCapabilities: held.ScopedCapabilities(),
		SetupPending:       len(steps) > 0,
		SetupRequired:      steps,
		CredentialEpoch:    account.CredentialEpoch,
	}, nil
}

// Setup steps an account may still owe before its sessions reach anything.
const (
	setupStepPassword  = "password"
	setupStepTwoFactor = "two_factor"
)

// accountSetupSteps lists what the account must still do before its sessions
// reach anything: replace a password someone else chose, and enroll a TOTP
// authenticator where 2FA is required. Empty means the account is set up.
func accountSetupSteps(store twoFactorStore, account *models.Mailbox) ([]string, error) {
	var steps []string
	if account.PasswordChangeRequired {
		steps = append(steps, setupStepPassword)
	}
	if account.TwoFactorRequired {
		if store == nil {
			// No way to check an enrollment: fail closed, the account stays in setup.
			return append(steps, setupStepTwoFactor), nil
		}
		_, err := store.GetActive(account.ID)
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
	// accountState re-derives live account state (active flag, capabilities)
	// on refresh. Nil in DB-less unit tests, which then trust the
	// refresh-token claims unchanged.
	accountState accountStateStore
	// twoFactorStore + masterKey back the optional TOTP 2FA enforcement in the
	// login path (OSI-19). When an account has 2FA active, a correct password is
	// necessary but not sufficient — a valid current code (or recovery code) is
	// also required before tokens are issued. masterKey decrypts the stored
	// secrets. A nil store (no DB, some unit tests) disables the check.
	twoFactorStore twoFactorStore
	masterKey      string
	// pushDevices ends the push devices of a session a logout ends (#363).
	// Nil when push is off.
	pushDevices sessionDevices
	// sessions is the cache RejectStaleSessions reads; a password change or
	// a reset ends the account's sessions, and empties its entry.
	sessions *SessionCache
	// recoverFailures is the budget of refused recoveries per address (#124),
	// on top of the per-IP auth throttle. Nil leaves it unlimited.
	recoverFailures *middleware.FailureBudget
}

// sessionDevices is what a logout needs of the push device store: to delete
// the devices of a session once it has ended.
type sessionDevices interface {
	SessionEnded(sid string, now time.Time) (int, error)
}

// SetRecoverFailures sets the budget of refused recoveries per address. Call
// before the router serves traffic.
func (h *AuthHandler) SetRecoverFailures(budget *middleware.FailureBudget) {
	h.recoverFailures = budget
}

// SetPushDevices makes a logout delete the push devices of the session it
// ends. Call before the router serves traffic.
func (h *AuthHandler) SetPushDevices(devices sessionDevices) {
	h.pushDevices = devices
}

// NewAuthHandler wires the auth handler. masterKey is the MASTER_KEY used to
// decrypt stored TOTP secrets during login enforcement; pass "" when
// encryption is not configured (2FA enrollment is then refused elsewhere, and
// login enforcement degrades to recovery-code-only for any pre-existing
// enrollment — a secret that could not have been stored anyway). sessions is
// the router's session cache; nil when there is none.
func NewAuthHandler(db *gorm.DB, jwtService *auth.JWTService, masterKey string, sessions *SessionCache) *AuthHandler {
	var refreshStore refreshTokenStore
	var tfStore twoFactorStore
	var accountState accountStateStore
	if db != nil {
		refreshStore = repositories.NewRefreshTokenRepository(db)
		tfStore = repositories.NewTwoFactorRepository(db)
		accountState = dbAccountState{db: db}
	}
	return &AuthHandler{db: db, jwtService: jwtService, refreshStore: refreshStore, accountState: accountState, twoFactorStore: tfStore, masterKey: masterKey, sessions: sessions}
}

// persistRefreshToken records a freshly issued refresh token in the rotation
// ledger (status active). A nil store (no database, e.g. some unit tests) is a
// no-op — production always wires the DB-backed store via NewAuthHandler.
func (h *AuthHandler) persistRefreshToken(tokens *auth.TokenPair, accountID uint) error {
	if h.refreshStore == nil {
		return nil
	}
	return h.refreshStore.Save(&models.RefreshToken{
		Jti:       tokens.RefreshJTI,
		AccountID: accountID,
		SessionID: tokens.SessionID,
		Status:    models.RefreshTokenActive,
		ExpiresAt: tokens.RefreshExpiresAt,
	})
}

// revokeAllForAccount best-effort revokes every remaining active session of one
// account. Used from the refresh path when a disabled/deleted account is
// detected; failures are non-fatal (the refresh is refused regardless) but
// logged.
func (h *AuthHandler) revokeAllForAccount(accountID uint) {
	if h.refreshStore == nil {
		return
	}
	if err := h.refreshStore.RevokeAllForAccount(accountID); err != nil {
		slog.Warn("failed to revoke sessions on refresh", "account_id", accountID, "error", err)
	}
}

type loginRequest = apitypes.LoginRequest

// clientNative is the loginRequest.Client value that asks for body tokens.
const clientNative = "native"

// refreshRequest is the body a native client sends to /auth/refresh and
// /auth/logout. Browsers send no body; their refresh token is the cookie.
type refreshRequest struct {
	RefreshToken string `json:"refresh_token,omitempty"`
}

// sessionTokenRequest is the optional body of /auth/refresh and /auth/logout: a native
// client's refresh token. A browser sends none; its token is the cookie.
type sessionTokenRequest struct {
	Body *refreshRequest `body:""`
}

// loginResponse is the login/refresh success body. For a browser it carries NO
// tokens: they are delivered ONLY as httpOnly cookies (see deliverSession), so
// page JavaScript — and therefore any XSS — can neither read them at rest nor
// scrape them from this response. ExpiresIn lets the SPA schedule a pre-emptive
// refresh without ever seeing the token; User and Capabilities let it restore
// session UI state on boot from /auth/refresh. Capabilities are what the
// account holds at "*", from its grants, for the client to show what it can
// do; they authorise nothing (the API reads the grants on every request).
//
// The token fields are filled only for a native client: one that logged in with
// "client": "native", or refreshed by sending its refresh token in the body. A
// request that arrives with the refresh cookie is a browser's and never gets
// them, whatever its body says.
type loginResponse = apitypes.LoginResponse

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
func presentedRefreshToken(r *http.Request, body *refreshRequest) (token string, native bool) {
	if c, err := r.Cookie(auth.RefreshCookieName); err == nil && c.Value != "" {
		return c.Value, false
	}
	if body != nil && body.RefreshToken != "" {
		return body.RefreshToken, true
	}
	return "", false
}

// headerSink is a ResponseWriter that keeps only the headers written to it: the cookie
// helpers set cookies on it, and a registered route answers them (endpoint.Headed).
type headerSink http.Header

func (s headerSink) Header() http.Header       { return http.Header(s) }
func (headerSink) Write(b []byte) (int, error) { return len(b), nil }
func (headerSink) WriteHeader(int)             {}

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

// userInfo is the account a session is for: its id (the mailbox's, the
// {id} of /api/v1/accounts/{id}/…) and address.
type userInfo = apitypes.UserInfo

// Login signs an account in: its address, a password matching one of its
// password credentials, and a code from one of its TOTP credentials when it
// has one.
func (h *AuthHandler) login(r *http.Request, req loginRequest) (endpoint.Headed[loginResponse], error) {
	var zero endpoint.Headed[loginResponse]

	// Find the account. A miss does NOT short-circuit: MatchPassword still runs
	// one bcrypt comparison (against a dummy hash) so an unknown address, an
	// account with no password and a wrong password are indistinguishable by
	// timing or message (OSI-24 user-enumeration defense-in-depth).
	var account models.Mailbox
	lookupErr := repositories.FindMailboxByAddress(h.db, req.Email, &account)
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to verify the password")
	}
	now := time.Now()
	cred, err := repositories.NewCredentialRepository(h.db).MatchPassword(account.ID, req.Password, now)
	if err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to verify the password")
	}
	if lookupErr != nil || cred == nil {
		return zero, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Invalid email or password")
	}

	// Credentials verified. Account-state checks (which legitimately surface a
	// distinct message) run only after a correct password, so they never leak
	// account existence to an unauthenticated guesser.
	if !account.Active {
		return zero, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Account is disabled")
	}
	if account.BootstrapExpired(now) {
		return zero, endpoint.Refuse(http.StatusUnauthorized, "bootstrap_expired",
			"This server's bootstrap password has expired. Deploy it again with a new one, then sign in with that.")
	}

	// Second factor (OSI-19): only reached with a correct password, so a 2FA
	// challenge never leaks account existence. No-op for accounts without 2FA.
	verified, err := h.enforce2FA(account.ID, req)
	if err != nil {
		return zero, err
	}

	return h.newSession(&account, req.Client == clientNative, signInMethods(verified))
}

// enforce2FA gates token issuance on the second factor when an account has TOTP
// 2FA active. It MUST be called only after the password has been verified: the
// enrollment lookup happens here, so whether 2FA is enabled never leaks to a
// caller who has not already proven the password (OSI-19 + OSI-24). Returns
// true when login may proceed (no active 2FA, or a valid TOTP/recovery code was
// supplied), and whether a second factor was verified. Otherwise it refuses — with a
// distinct totp_required code when no second factor was supplied, so a client knows to
// prompt for one.
func (h *AuthHandler) enforce2FA(accountID uint, req loginRequest) (verified bool, err error) {
	if h.twoFactorStore == nil {
		return false, nil
	}
	_, err = h.twoFactorStore.GetActive(accountID)
	if errors.Is(err, repositories.ErrTwoFactorNotFound) {
		return false, nil // account has no active 2FA — unchanged behaviour
	}
	if err != nil {
		return false, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to verify two-factor authentication")
	}
	if verifyTOTPOrRecovery(h.twoFactorStore, h.masterKey, accountID, req.TOTPCode, req.RecoveryCode) {
		return true, nil
	}
	if req.TOTPCode == "" && req.RecoveryCode == "" {
		return false, endpoint.Refuse(http.StatusUnauthorized, "totp_required", "Two-factor authentication code required")
	}
	return false, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Invalid two-factor authentication code")
}

// signInMethods is the amr of a session that signed in with a password, and a
// second factor if verified: a TOTP code or a recovery code.
func signInMethods(verified bool) []string {
	if verified {
		return []string{auth.AMRPassword, auth.AMROTP}
	}
	return []string{auth.AMRPassword}
}

func (h *AuthHandler) logout(r *http.Request, in sessionTokenRequest) (endpoint.Headed[endpoint.Empty], error) {
	// Revoke the presented refresh token server-side so it can no longer be
	// exchanged (OSI-10: logout was previously client-side only). The token is
	// the cookie, or for a native client the body. A missing or invalid token
	// still clears client state below. A storage failure keeps the cookies for
	// retry and reports the failure. Idempotent, so a double
	// logout is harmless. A body whose refresh_token is not a string is a
	// client bug, refused by binding with 422 rather than silently treated as
	// no token.
	if token, _ := presentedRefreshToken(r, in.Body); token != "" && h.refreshStore != nil {
		if claims, err := h.jwtService.ValidateRefreshToken(token); err == nil && claims.ID != "" {
			if err := h.refreshStore.Revoke(claims.ID); err != nil {
				return endpoint.Headed[endpoint.Empty]{}, fmt.Errorf("revoke the signed-out session: %w", err)
			}
			// The phones the session registered for push go with it.
			if h.pushDevices != nil {
				if _, err := h.pushDevices.SessionEnded(claims.SessionID, time.Now()); err != nil {
					slog.Warn("failed to remove the push devices of a session that signed out", "error", err)
				}
			}
		}
	}

	cookies := headerSink{}
	http.SetCookie(cookies, &http.Cookie{
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
	clearSessionCookies(cookies)
	return endpoint.Headed[endpoint.Empty]{Header: http.Header(cookies)}, nil
}

func (h *AuthHandler) refresh(r *http.Request, in sessionTokenRequest) (endpoint.Headed[loginResponse], error) {
	var zero endpoint.Headed[loginResponse]
	token, native := presentedRefreshToken(r, in.Body)
	if token == "" {
		return zero, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "No refresh token")
	}

	claims, err := h.jwtService.ValidateRefreshToken(token)
	if err != nil {
		return zero, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Invalid or expired refresh token")
	}

	// Re-read live account state BEFORE consuming the rotation, so a token that
	// outlived a password change, disable or delete cannot mint a fresh access
	// token, and the capabilities the response lists are the grants' now. A nil
	// store (no DB, some unit tests) trusts the claims unchanged.
	//
	// Like the rotation-ledger check below, this path fails CLOSED: an
	// unreachable store mints nothing. It answers 500, not 401: the failure
	// is the server's, and a 401 would tell the client its session is over
	// and to discard the refresh token it can still use once the store is
	// back.
	state := accountState{
		Active:          true,
		Address:         claims.Address,
		DisplayName:     claims.Address,
		SetupPending:    claims.SetupPending,
		CredentialEpoch: claims.CredentialEpoch,
	}
	if h.accountState != nil {
		var stateErr error
		state, stateErr = h.accountState.AccountState(claims.AccountID)
		if stateErr != nil {
			slog.Error("refresh: could not read the account's state", "error", stateErr)
			return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to verify the session")
		}
		if !state.Active {
			h.revokeAllForAccount(claims.AccountID)
			return zero, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Account is disabled")
		}
		// A refresh token from before the account's sessions last ended (a
		// new password, a reset, 2FA turned off, an admin ending them) is as
		// dead as the access tokens of its time: it must not mint one at the
		// new epoch.
		if state.CredentialEpoch != claims.CredentialEpoch {
			return zero, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "This session has ended; sign in again")
		}
	}

	// Rotation + revocation (OSI-10): the presented refresh token must still be
	// the ACTIVE ledger row for its jti. Rotate flips active→rotated atomically;
	// it fails (not-found) when the row is missing, already rotated (reuse of a
	// spent token), or revoked (logout / password change). Any of those refuses
	// the refresh. A nil store (no DB, some unit tests) skips this check.
	if h.refreshStore != nil {
		if err := h.refreshStore.Rotate(claims.ID); errors.Is(err, repositories.ErrRefreshTokenNotFound) {
			return zero, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Invalid or expired refresh token")
		} else if err != nil {
			slog.Error("refresh: could not rotate the refresh token", "error", err)
			return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to verify the session")
		}
	}

	// A refresh keeps the session's sign-in methods: finishing 2FA setup and
	// refreshing is not signing in with 2FA. A session whose account is in
	// setup stays that way until the setup is done: the refresh must not be a
	// way out of the restriction. The new tokens stay in the session: the
	// same sid.
	tokens, err := h.jwtService.GenerateSession(claims.AccountID, state.Address, state.CredentialEpoch, claims.AMR, state.SetupPending, auth.ContinueSession(claims.SessionID))
	if err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to generate tokens")
	}

	// Record the replacement refresh token as the new active ledger row.
	if err := h.persistRefreshToken(tokens, claims.AccountID); err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to persist session")
	}

	// For a browser the body carries only what the SPA needs to restore session
	// UI on boot (identity + capabilities + expiry) and the tokens go out as
	// cookies; a native client gets them in the body, on the same channel its
	// refresh token came in on.
	resp := loginResponse{
		ExpiresIn: tokens.ExpiresIn,
		User:      userInfo{ID: claims.AccountID, Email: state.Address, DisplayName: state.DisplayName},
	}
	if state.SetupPending {
		resp.SetupRequired = state.SetupRequired
		// A store-less handler is used in token-only tests. If it cannot
		// distinguish the steps, keep the client in setup conservatively.
		if len(resp.SetupRequired) == 0 {
			resp.SetupRequired = []string{setupStepPassword, setupStepTwoFactor}
		}
	} else {
		resp.Capabilities = state.Capabilities
		resp.ScopedCapabilities = state.ScopedCapabilities
	}
	cookies := headerSink{}
	if err := deliverSession(cookies, tokens, native, &resp); err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to establish session")
	}
	return endpoint.Headed[loginResponse]{Header: http.Header(cookies), Value: resp}, nil
}

// minChosenPasswordLength is bootstrap's, so every way of choosing an admin
// password asks the same of it.
const minChosenPasswordLength = bootstrap.MinChosenPasswordLength

// sessionCookies is how a browser's session is delivered, rotated and ended: the httpOnly
// restmail_access and restmail_refresh cookies and the readable restmail_csrf one. A native
// client gets the tokens in the body instead, and no cookies.
var sessionCookies = map[string]oapifly.ResponseHeader{
	"Set-Cookie": {Description: "For a browser: restmail_access and restmail_refresh (httpOnly) and restmail_csrf, set or, on logout, expired"},
}

// RegisterSignIn registers signing in, refreshing the session, signing out and recovering a
// forgotten password. None takes a credential; sign-in, refresh and recovery check one, so
// they are throttled like login, and each is audited as it always was.
func (h *AuthHandler) RegisterSignIn(rs endpoint.Routes, checks SessionRouteChecks) {
	tags := []string{"Auth"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/login",
		OperationID: "login", Summary: "Sign an account in with its address and password", Tags: tags,
		Security: oapifly.Public, Headers: sessionCookies, Errors: []int{http.StatusUnauthorized},
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.sign_in", middleware.AuditSignIn())}}, h.login)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/logout",
		OperationID: "logout", Summary: "Log out — revoke the session and clear the auth cookies", Tags: tags,
		Status: http.StatusNoContent, Security: oapifly.Public, Headers: sessionCookies,
		Middleware: []endpoint.Middleware{checks.Audit("auth.sign_out", middleware.AuditRefreshToken(), middleware.AuditIdentifiedOnly())}}, h.logout)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/refresh",
		OperationID: "refreshToken", Summary: "Refresh the session (cookie, or body token for native clients)", Tags: tags,
		Description: "The refresh token rotates: the one presented stops working, and a reused one ends the session.",
		Security:    oapifly.Public, Headers: sessionCookies, Errors: []int{http.StatusUnauthorized},
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.refresh", middleware.AuditRefreshToken(), middleware.AuditFailuresOnly())}}, h.refresh)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/reset",
		OperationID: "resetAdmin", Summary: "Let an account back in with a reset token; also hands a server over", Tags: tags,
		Description: "Redeems a single-use reset_admin token (restmail reset-admin, or an owner key's). The account it names " +
			"gets the password the redeemer chose, loses its authenticator and must enroll a new one, and its app passwords " +
			"and every token it was issued are dead at once. A password under 12 bytes is 422 and does not spend the token.",
		Security: oapifly.Public, Headers: sessionCookies, Errors: []int{http.StatusUnauthorized},
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.reset", middleware.AuditStartedSession())}}, h.reset)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/recover",
		OperationID: "recoverAccount", Summary: "Reset a forgotten password with the authenticator and a recovery code", Tags: tags,
		Description: "Each address may be refused 5 times, then once per 15 minutes: 429 rate_limited with Retry-After.",
		Security:    oapifly.Public, Headers: sessionCookies, Errors: []int{http.StatusUnauthorized, http.StatusTooManyRequests},
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.recover", middleware.AuditStartedSession())}}, h.recoverAccount)
}

// RegisterPasswordChange registers an account replacing its own password, which a session
// still in setup reaches. It checks a password, so it is throttled like login.
func (h *AuthHandler) RegisterPasswordChange(rs endpoint.Routes, checks SessionRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/password",
		OperationID: "changeOwnPassword", Summary: "Replace your own password", Tags: []string{"Auth"},
		Errors:     []int{http.StatusForbidden},
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.password_change")}}, h.changePassword)
}

// changePasswordRequest is the body of POST /api/v1/auth/password. The new
// password's length is counted in bytes, as bootstrap counts it, and only once
// the current password is proven, so it is the handler's to check.
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// passwordChanged is the answer to a changed password: sign in again.
type passwordChanged struct {
	Message string `json:"message"`
}

// changePassword lets a signed-in account replace its own password, and is
// the one thing a session flagged password_change_required may do besides
// enrolling 2FA. It needs the current password even then, so a stolen access
// token alone cannot take the account. Success clears the flag, revokes every
// app password (whoever knew the old password could have made one) and every
// session of the account, including this one: the next sign-in gets the
// account's real capabilities.
// POST /api/v1/auth/password
func (h *AuthHandler) changePassword(r *http.Request, req changePasswordRequest) (passwordChanged, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return passwordChanged{}, errAuthenticationRequired
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		return passwordChanged{}, endpoint.Invalid(map[string]string{
			"current_password": "required",
			"new_password":     "required",
		})
	}

	var account models.Mailbox
	if err := h.db.First(&account, claims.AccountID).Error; err != nil || !account.Active {
		return passwordChanged{}, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Invalid or expired session")
	}
	now := time.Now()
	cred, err := repositories.NewCredentialRepository(h.db).MatchPassword(account.ID, req.CurrentPassword, now)
	if err != nil {
		return passwordChanged{}, fmt.Errorf("verify the password: %w", err)
	}
	if cred == nil {
		// 403, never 401: the session is still good, only the password is wrong.
		return passwordChanged{}, endpoint.Refuse(http.StatusForbidden, "invalid_password", "Current password is incorrect")
	}
	if len(req.NewPassword) < minChosenPasswordLength {
		return passwordChanged{}, endpoint.Invalid(map[string]string{
			"new_password": fmt.Sprintf("must be at least %d characters", minChosenPasswordLength),
		})
	}
	// The point of a forced change is a credential nobody else has seen.
	if req.NewPassword == req.CurrentPassword {
		return passwordChanged{}, endpoint.Invalid(map[string]string{"new_password": "must differ from the current password"})
	}

	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		return passwordChanged{}, fmt.Errorf("hash the password: %w", err)
	}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Mailbox{}).Where("id = ?", account.ID).Updates(map[string]interface{}{
			"last_password_change":     now,
			"password_change_required": false,
			// Its own password now: the deploy-time one, and its deadline, are gone.
			"bootstrap_expires_at": nil,
		}).Error; err != nil {
			return err
		}
		// Every token of the account is dead from now, this one included.
		return setAccountPassword(tx, r, account.ID, hash, &account.ID, now)
	})
	if err != nil {
		return passwordChanged{}, fmt.Errorf("set the password: %w", err)
	}
	h.sessions.Forget(account.ID)
	return passwordChanged{Message: "Password changed. Sign in again with the new password."}, nil
}

// setAccountPassword makes hash the account's one password, in tx: its other
// passwords and every app password are revoked (each app password recorded in
// the audit log, as revoked for a new password), and every session ends
// (endSessions). setBy is the account setting it.
func setAccountPassword(tx *gorm.DB, r *http.Request, accountID uint, hash string, setBy *uint, now time.Time) error {
	if _, err := repositories.NewCredentialRepository(tx).SetPassword(accountID, hash, setBy, now); err != nil {
		return err
	}
	if err := revokeAppPasswordsForNewPassword(tx, r, accountID, now); err != nil {
		return err
	}
	return endSessions(tx, accountID)
}

// newSession is a fresh session for an account whose identity is
// already proven — by password and second factor at login, or by a single-use
// token at reset — with amr saying how. An account still in setup gets a
// session that can only finish the setup.
func (h *AuthHandler) newSession(account *models.Mailbox, native bool, amr []string) (endpoint.Headed[loginResponse], error) {
	var zero endpoint.Headed[loginResponse]
	// What the grants give at "*", for the client to show.
	held, err := grants.Load(h.db, account.ID, time.Now())
	if err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to load the account's capabilities")
	}
	capNames := held.Capabilities()
	scoped := held.ScopedCapabilities()

	setupSteps, err := accountSetupSteps(h.twoFactorStore, account)
	if err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to read account state")
	}
	if len(setupSteps) > 0 {
		capNames = nil
		scoped = nil
	}
	tokens, err := h.jwtService.GenerateSession(account.ID, account.Address, account.CredentialEpoch, amr, len(setupSteps) > 0)
	if err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to generate tokens")
	}

	// Record the refresh token in the rotation ledger before handing it out, so
	// it can later be rotated on use and revoked on logout (OSI-10).
	if err := h.persistRefreshToken(tokens, account.ID); err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to persist session")
	}

	resp := loginResponse{
		ExpiresIn:          tokens.ExpiresIn,
		Capabilities:       capNames,
		ScopedCapabilities: scoped,
		SetupRequired:      setupSteps,
		User: userInfo{
			ID:          account.ID,
			Email:       account.Address,
			DisplayName: account.DisplayName,
		},
	}
	cookies := headerSink{}
	if err := deliverSession(cookies, tokens, native, &resp); err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to establish session")
	}

	h.db.Model(account).Update("last_login_at", time.Now())

	return endpoint.Headed[loginResponse]{Header: http.Header(cookies), Value: resp}, nil
}

// resetRequest is the body of POST /api/v1/auth/reset. Redeeming checks the token and the
// password: a missing token is the 401 a wrong one is, a missing password the 422 a short one
// is.
type resetRequest = apitypes.ResetRequest

// reset redeems a reset_admin token — minted by `restmail-api mint-token
// --purpose reset-admin`, or for an owner key. The account it names gets the
// password the redeemer chose, loses its authenticator and must enroll a new
// one, and every session and app password it had ends: whoever held the old
// credentials, a person or an agent that set the server up, is locked out.
func (h *AuthHandler) reset(_ *http.Request, req resetRequest) (endpoint.Headed[loginResponse], error) {
	var zero endpoint.Headed[loginResponse]
	if req.Client != "" && req.Client != clientNative {
		return zero, endpoint.Invalid(map[string]string{"client": `must be "native" or omitted`})
	}
	account, err := bootstrap.ResetAdmin(h.db, req.Token, req.Password)
	if err != nil {
		return zero, redeemRefusal(err)
	}
	h.sessions.Forget(account.ID)
	// Redeeming a token is neither a password nor a second factor: the
	// session records neither, and is in setup until a new authenticator is
	// enrolled anyway.
	return h.newSession(account, req.Client == clientNative, nil)
}

// redeemRefusal is the answer for a token that could not be redeemed.
func redeemRefusal(err error) error {
	switch {
	case errors.Is(err, bootstrap.ErrTokenInvalid):
		return endpoint.Refuse(http.StatusUnauthorized, "invalid_token", err.Error())
	case errors.Is(err, bootstrap.ErrPasswordTooShort):
		return endpoint.Invalid(map[string]string{"password": err.Error()})
	}
	slog.Error("redeeming a one-time token failed", "error", err)
	return endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to redeem the token")
}

// SessionCurrent is the database's answer to whether an access token is still
// current (middleware.RejectStaleSessions), read afresh on every call: the
// account must exist and be active, not be a bootstrap account whose
// deploy-time password has expired, and the token must carry its current
// credential epoch, which endSessions moves on. The router reads it through a
// SessionCache.
func SessionCurrent(db *gorm.DB) middleware.SessionCurrent {
	return func(accountID uint, epoch int) (bool, error) {
		account, err := sessionAccount(db, accountID)
		if err != nil {
			return false, err
		}
		return sessionCurrent(account, epoch, time.Now()), nil
	}
}

// recoverRequest is the body of POST /api/v1/auth/recover.
type recoverRequest struct {
	Email        string `json:"email,omitempty"`
	TOTPCode     string `json:"totp_code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
	NewPassword  string `json:"new_password,omitempty"`
	Client       string `json:"client,omitempty"`
}

// Recover is self-service password reset (#124). Mail cannot carry a reset
// link to someone locked out of the mailbox it would go to, so it accepts only
// what the account set up in advance: a code from its authenticator and one of
// its recovery codes, both. The authenticator is checked first, so a wrong one
// never spends a recovery code. Success sets the new password and ends every
// session and app password, like a password change, and signs in; the
// authenticator stays. An account without 2FA has no self-service path: an
// admin resets it. Every refusal looks the same, so it says nothing about
// which addresses exist or have 2FA.
func (h *AuthHandler) recoverAccount(r *http.Request, req recoverRequest) (endpoint.Headed[loginResponse], error) {
	var zero endpoint.Headed[loginResponse]
	problems := map[string]string{}
	if strings.TrimSpace(req.Email) == "" {
		problems["email"] = "required: the account's address"
	}
	if req.TOTPCode == "" {
		problems["totp_code"] = "required: a code from the account's authenticator"
	}
	if req.RecoveryCode == "" {
		problems["recovery_code"] = "required: one of the account's recovery codes"
	}
	if len(req.NewPassword) < minChosenPasswordLength {
		problems["new_password"] = fmt.Sprintf("must be at least %d characters", minChosenPasswordLength)
	}
	if req.Client != "" && req.Client != clientNative {
		problems["client"] = `must be "native" or omitted`
	}
	if len(problems) > 0 {
		return zero, endpoint.Invalid(problems)
	}
	// Refusals are charged to the address, known or not, so the budget says
	// nothing about which addresses exist; once it is spent, the address is
	// answered 429 without a check, from any IP.
	address := strings.ToLower(strings.TrimSpace(req.Email))
	if h.recoverFailures != nil {
		if ok, retry := h.recoverFailures.Available(address); !ok {
			return zero, &endpoint.Error{Status: http.StatusTooManyRequests, Code: "rate_limited",
				Message: "Too many failed recoveries for this address; try again later",
				Header:  http.Header{"Retry-After": {strconv.Itoa(max(1, int(math.Ceil(retry.Seconds()))))}}}
		}
	}
	refuse := func() error {
		if h.recoverFailures != nil {
			h.recoverFailures.Charge(address)
		}
		return endpoint.Refuse(http.StatusUnauthorized, "recovery_failed", "Invalid address, authenticator code or recovery code")
	}
	if h.twoFactorStore == nil {
		return zero, refuse()
	}

	var account models.Mailbox
	err := h.db.Where("address = ?", address).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return zero, refuse()
	}
	if err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to read the account")
	}
	if !account.Active ||
		!verifyTOTPOrRecovery(h.twoFactorStore, h.masterKey, account.ID, req.TOTPCode, "") ||
		!verifyTOTPOrRecovery(h.twoFactorStore, h.masterKey, account.ID, "", req.RecoveryCode) {
		return zero, refuse()
	}

	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to set password")
	}
	now := time.Now()
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Mailbox{}).Where("id = ?", account.ID).Updates(map[string]interface{}{
			"last_password_change":     now,
			"password_change_required": false,
			"bootstrap_expires_at":     nil,
		}).Error; err != nil {
			return err
		}
		return setAccountPassword(tx, r, account.ID, hash, &account.ID, now)
	})
	if err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to set password")
	}
	h.sessions.Forget(account.ID)
	// The new session carries the moved-on credential epoch.
	if err := h.db.First(&account, account.ID).Error; err != nil {
		return zero, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to read the account")
	}
	return h.newSession(&account, req.Client == clientNative, signInMethods(true))
}
