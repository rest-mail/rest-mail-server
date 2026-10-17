package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// maxCredentialNameLength caps a credential's name, in characters.
const maxCredentialNameLength = 100

// CredentialHandler serves a mailbox's credentials (#344): app passwords, which
// mail clients and automations use instead of the mailbox's own password.
//
// Listing, creating and revoking need a session from signing in with the
// mailbox's password: the routes accept no app password, so an app password
// can never mint or revoke one. Creating also needs the mailbox's current
// password, entered again, and its second factor when the mailbox has 2FA.
type CredentialHandler struct {
	db        *gorm.DB
	repo      *repositories.CredentialRepository
	twoFactor twoFactorStore
	now       func() time.Time
}

func NewCredentialHandler(db *gorm.DB) *CredentialHandler {
	return &CredentialHandler{
		db:        db,
		repo:      repositories.NewCredentialRepository(db),
		twoFactor: repositories.NewTwoFactorRepository(db),
		now:       time.Now,
	}
}

// credentialResponse is a credential as its owner sees it. Never the hash.
type credentialResponse struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	PublicID   string     `json:"public_id"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

func toCredentialResponse(c *models.Credential) credentialResponse {
	return credentialResponse{
		ID:         c.ID,
		Name:       c.Name,
		Kind:       c.Kind,
		PublicID:   c.PublicID,
		CreatedAt:  c.CreatedAt,
		LastUsedAt: c.LastUsedAt,
		ExpiresAt:  c.ExpiresAt,
	}
}

// createdAppPassword is the answer to creating one: the credential, and the
// app password itself, shown this once and never again.
type createdAppPassword struct {
	credentialResponse
	Password string `json:"password"`
}

// managedAccount resolves /accounts/{id} for the credential routes: the
// session's own account, or one it holds mailbox:manage on (one it owns,
// such as an automation's account with no password of its own). It writes the
// error and returns false otherwise; an account the session cannot manage is
// answered as one that does not exist.
func (h *CredentialHandler) managedAccount(w http.ResponseWriter, r *http.Request) (*auth.Claims, uint, bool) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return nil, 0, false
	}
	// The routes take no credential over HTTP Basic, but a credential must
	// never manage credentials, whatever the route says.
	if claims.CredentialID != 0 {
		respond.Error(w, http.StatusForbidden, "forbidden", "Sign in with your password to manage app passwords")
		return nil, 0, false
	}
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return nil, 0, false
	}
	if _, err := mayReach(h.db, claims, uint(id), models.CapMailboxManage); err != nil {
		if errors.Is(err, errNoAccess) {
			respond.Error(w, http.StatusNotFound, "not_found", "Account not found")
		} else {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to read the account")
		}
		return nil, 0, false
	}
	return claims, uint(id), true
}

// List returns the mailbox's app passwords that are not revoked.
// GET /api/v1/accounts/{id}/credentials
func (h *CredentialHandler) List(w http.ResponseWriter, r *http.Request) {
	_, accountID, ok := h.managedAccount(w, r)
	if !ok {
		return
	}
	creds, err := h.repo.ListForAccount(accountID, models.CredentialKindAppPassword)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list credentials")
		return
	}
	out := make([]credentialResponse, len(creds))
	for i := range creds {
		out[i] = toCredentialResponse(&creds[i])
	}
	respond.List(w, out, nil)
}

type createCredentialRequest struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at"`
	// Password is the mailbox's current password, entered again. It is
	// checked and never stored, logged or answered with.
	Password string `json:"password"`
}

// Create makes an app password and answers with it, once.
// POST /api/v1/accounts/{id}/credentials
func (h *CredentialHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims, accountID, ok := h.managedAccount(w, r)
	if !ok {
		return
	}
	var req createCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	now := h.now()
	switch {
	case req.Name == "":
		respond.ValidationError(w, map[string]string{"name": "required"})
		return
	case utf8.RuneCountInString(req.Name) > maxCredentialNameLength:
		respond.ValidationError(w, map[string]string{"name": fmt.Sprintf("must be at most %d characters", maxCredentialNameLength)})
		return
	case strings.IndexFunc(req.Name, unicode.IsControl) >= 0:
		respond.ValidationError(w, map[string]string{"name": "must not contain control characters"})
		return
	case req.ExpiresAt != nil && !req.ExpiresAt.After(now):
		respond.ValidationError(w, map[string]string{"expires_at": "must be in the future"})
		return
	}

	// Creating a credential needs a person who knows the mailbox's password
	// now. A session outlives a change of password by up to its access token's
	// lifetime, and an app password made in that window, by whoever learnt the
	// old password, would outlive the change.
	if !h.checkCurrentPassword(w, claims.AccountID, req.Password) {
		return
	}

	// It also needs a session whose sign-in took the mailbox's second factor,
	// when the mailbox has one.
	_, err := h.twoFactor.GetActive(claims.AccountID)
	switch {
	case err == nil && !claims.TwoFactor:
		respond.Error(w, http.StatusForbidden, "two_factor_required",
			"This mailbox has two-factor authentication: sign in again with your code to create an app password")
		return
	case err != nil && !errors.Is(err, repositories.ErrTwoFactorNotFound):
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to verify two-factor authentication")
		return
	}

	cred, password, err := h.repo.CreateAppPassword(accountID, claims.AccountID, req.Name, req.ExpiresAt)
	if err != nil {
		slog.Error("creating an app password failed", "account_id", accountID, "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create the app password")
		return
	}
	respond.Data(w, http.StatusCreated, createdAppPassword{credentialResponse: toCredentialResponse(cred), Password: password})
}

// checkCurrentPassword checks password against the mailbox's password hash
// as it is now, with the same bcrypt comparison as signing in. A mailbox that
// is gone or disabled is compared against a dummy hash, so it costs the same,
// and refused. It writes the error and returns false unless password matches.
func (h *CredentialHandler) checkCurrentPassword(w http.ResponseWriter, accountID uint, password string) bool {
	if password == "" {
		respond.Error(w, http.StatusUnauthorized, "reauth_required",
			"Enter your current password to create an app password")
		return false
	}
	var account models.Mailbox
	err := h.db.Select("id").Where("id = ? AND active = ?", accountID, true).Take(&account).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to verify the password")
		return false
	}
	cred, matchErr := h.repo.MatchPassword(account.ID, password, h.now())
	if matchErr != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to verify the password")
		return false
	}
	if cred == nil || err != nil {
		respond.Error(w, http.StatusUnauthorized, "invalid_password", "The password is incorrect")
		return false
	}
	return true
}

// Revoke revokes one of the mailbox's app passwords. Its row stays, revoked,
// for the audit trail; the next request made with it is refused.
// DELETE /api/v1/accounts/{id}/credentials/{credential_id}
func (h *CredentialHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	_, accountID, ok := h.managedAccount(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(chi.URLParam(r, "credential_id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid credential ID")
		return
	}
	err = h.repo.Revoke(accountID, uint(id), h.now())
	if errors.Is(err, repositories.ErrCredentialNotFound) {
		respond.Error(w, http.StatusNotFound, "not_found", "Credential not found")
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to revoke the credential")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CredentialRevokedPasswordChanged is the reason recorded for an app password
// revoked because its mailbox's password changed.
const CredentialRevokedPasswordChanged = "password_changed"

// revokeAppPasswordsForNewPassword revokes every app password of a mailbox
// whose password r changes, in tx, the transaction that changes it. Whoever
// learnt the old password could have made one, and it must not outlive the
// password any more than a session does (#164). Each revocation is recorded
// as a credentials.revoke entry with the reason, in the same transaction, so
// the new password, the revocations and their entries commit together or not
// at all.
func revokeAppPasswordsForNewPassword(tx *gorm.DB, r *http.Request, accountID uint, now time.Time) error {
	revoked, err := repositories.NewCredentialRepository(tx).RevokeAppPasswords(accountID, now)
	if err != nil {
		return err
	}
	for _, cred := range revoked {
		e := middleware.NewAuditEntry(middleware.AuditActionCredentialsRevoke, r, now)
		e.Target["account_id"] = strconv.FormatUint(uint64(accountID), 10)
		e.Target["credential_id"] = strconv.FormatUint(uint64(cred.ID), 10)
		e.Target["public_id"] = cred.PublicID
		e.Target["name"] = cred.Name
		e.Target["reason"] = CredentialRevokedPasswordChanged
		if err := audit.Append(tx, e); err != nil {
			return err
		}
	}
	return nil
}

// signInProtocols are the protocols a gateway signs a mail client in over.
var signInProtocols = map[string]bool{"imap": true, "pop3": true, "smtp": true}

type protocolSignInRequest struct {
	Protocol string `json:"protocol"`
}

// ProtocolSignIn is how a protocol gateway signs in a mail client: the request
// carries the address and what the client gave as its password (an app
// password, or one of the account's passwords) with HTTP Basic, already
// checked by the middleware against the account's credentials, and the audit
// log records the sign-in with its credential. It answers with the account,
// as login does, so the gateway knows which account the requests it then
// makes, each carrying the same Basic credentials, are for. A session cannot
// use it.
// POST /api/v1/auth/protocol-sign-in
func (h *CredentialHandler) ProtocolSignIn(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil || claims.CredentialID == 0 {
		respond.Error(w, http.StatusBadRequest, "basic_required",
			"Sign in here with HTTP Basic: the account's address, and its password or an app password")
		return
	}
	var req protocolSignInRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if !signInProtocols[req.Protocol] {
		respond.ValidationError(w, map[string]string{"protocol": `must be "imap", "pop3" or "smtp"`})
		return
	}
	var account models.Mailbox
	if err := h.db.Select("id", "address", "display_name").Take(&account, claims.AccountID).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to load the account")
		return
	}
	respond.Data(w, http.StatusOK, map[string]any{
		"user": userInfo{
			ID:          account.ID,
			Email:       account.Address,
			DisplayName: account.DisplayName,
		},
		"credential_id":   claims.CredentialID,
		"credential_kind": claims.CredentialKind,
	})
}

// BasicVerifier checks credentials sent with HTTP Basic against the
// credentials table (an app password, or one of the account's passwords) for
// middleware.BasicCredentials, and builds the claims the request runs with:
// the account's, with the credential set. It records the credential's use, at
// most once a minute.
//
// A gateway sends the same Basic credentials on every call it makes for a
// mail client, so a password that has just been checked is remembered for a
// short while (passwordChecks) rather than costing a bcrypt comparison on
// every IMAP command. Only a match is remembered, keyed by the credential and
// its hash as well as the secret: a new password, a revoked one, or another
// password misses, and the credential's row is read on every request anyway.
func BasicVerifier(db *gorm.DB) middleware.BasicVerify {
	repo := repositories.NewCredentialRepository(db)
	checks := newPasswordChecks(passwordCheckTTL)
	return func(address, secret string) (*auth.Claims, error) {
		now := time.Now()
		cred, account, err := checks.verify(repo, address, secret, now)
		if err != nil {
			return nil, err
		}
		if cred.LastUsedAt == nil || now.Sub(*cred.LastUsedAt) >= repositories.LastUsedEvery {
			if err := repo.TouchLastUsed(cred.ID, now); err != nil {
				slog.Warn("could not record a credential's use", "credential_id", cred.ID, "error", err)
			}
		}
		return &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: fmt.Sprintf("account:%d", account.ID)},
			AccountID:        account.ID,
			Address:          account.Address,
			TokenType:        "access",
			CredentialID:     cred.ID,
			CredentialKind:   cred.Kind,
		}, nil
	}
}
