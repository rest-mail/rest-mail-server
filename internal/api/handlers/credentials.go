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
// mailbox's password, and creating also its second factor when the mailbox has
// 2FA: the routes accept no app password, so an app password can never mint or
// revoke one.
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

// ownMailbox resolves /accounts/{id} for the credential routes: only the
// session's own account, whose mailbox the session signed in to. A credential
// is a way into a mailbox, so for now only that mailbox may make one, never an
// account it has linked. It writes the error and returns false otherwise.
func (h *CredentialHandler) ownMailbox(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return nil, false
	}
	// The routes take no app password, but a credential must never manage
	// credentials, whatever the route says.
	if claims.UserType != "mailbox" || claims.CredentialID != 0 {
		respond.Error(w, http.StatusForbidden, "forbidden", "Only a mailbox, signed in with its password, manages its app passwords")
		return nil, false
	}
	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return nil, false
	}
	if uint(accountID) != claims.WebmailAccountID {
		respond.Error(w, http.StatusNotFound, "not_found", "Account not found")
		return nil, false
	}
	return claims, true
}

// List returns the mailbox's app passwords that are not revoked.
// GET /api/v1/accounts/{id}/credentials
func (h *CredentialHandler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.ownMailbox(w, r)
	if !ok {
		return
	}
	creds, err := h.repo.ListForMailbox(claims.MailboxID)
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
}

// Create makes an app password and answers with it, once.
// POST /api/v1/accounts/{id}/credentials
func (h *CredentialHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.ownMailbox(w, r)
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

	// Creating a credential needs a person: a session whose sign-in took the
	// mailbox's second factor, when the mailbox has one.
	_, err := h.twoFactor.GetActive(models.TwoFactorUserTypeMailbox, claims.MailboxID)
	switch {
	case err == nil && !claims.TwoFactor:
		respond.Error(w, http.StatusForbidden, "two_factor_required",
			"This mailbox has two-factor authentication: sign in again with your code to create an app password")
		return
	case err != nil && !errors.Is(err, repositories.ErrTwoFactorNotFound):
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to verify two-factor authentication")
		return
	}

	cred, password, err := h.repo.CreateAppPassword(claims.MailboxID, req.Name, req.ExpiresAt)
	if err != nil {
		slog.Error("creating an app password failed", "mailbox_id", claims.MailboxID, "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create the app password")
		return
	}
	respond.Data(w, http.StatusCreated, createdAppPassword{credentialResponse: toCredentialResponse(cred), Password: password})
}

// Revoke revokes one of the mailbox's app passwords. Its row stays, revoked,
// for the audit trail; the next request made with it is refused.
// DELETE /api/v1/accounts/{id}/credentials/{credential_id}
func (h *CredentialHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.ownMailbox(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(chi.URLParam(r, "credential_id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid credential ID")
		return
	}
	err = h.repo.Revoke(claims.MailboxID, uint(id), h.now())
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
func revokeAppPasswordsForNewPassword(tx *gorm.DB, r *http.Request, mailboxID uint, now time.Time) error {
	revoked, err := repositories.NewCredentialRepository(tx).RevokeAppPasswords(mailboxID, now)
	if err != nil {
		return err
	}
	for _, cred := range revoked {
		e := middleware.NewAuditEntry(middleware.AuditActionCredentialsRevoke, r, now)
		e.Target["mailbox_id"] = strconv.FormatUint(uint64(mailboxID), 10)
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

// ProtocolSignIn is how a protocol gateway signs in a mail client that gave it
// an app password: the request carries the address and the app password with
// HTTP Basic, already checked by the middleware, and the audit log records the
// sign-in with its credential. It answers with the account, as login does, so
// the gateway knows which account the requests it then makes, each carrying
// the same Basic credentials, are for. A session cannot use it.
// POST /api/v1/auth/protocol-sign-in
func (h *CredentialHandler) ProtocolSignIn(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil || claims.CredentialID == 0 {
		respond.Error(w, http.StatusBadRequest, "app_password_required",
			"Sign in here with an app password, sent with HTTP Basic as address:app_password")
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
	var mailbox models.Mailbox
	if err := h.db.Select("id", "address", "display_name").Take(&mailbox, claims.MailboxID).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to load the mailbox")
		return
	}
	respond.Data(w, http.StatusOK, map[string]any{
		"user": userInfo{
			ID:          claims.WebmailAccountID,
			Email:       mailbox.Address,
			DisplayName: mailbox.DisplayName,
		},
		"credential_id": claims.CredentialID,
	})
}

// AppPasswordVerifier checks app passwords against the credentials table, for
// middleware.AppPasswords: the claims it returns are the owning mailbox's, with
// CredentialID set, and with its webmail account, created as at a first
// sign-in when there is none yet. A use is recorded in last_used_at, at most
// once a minute.
func AppPasswordVerifier(db *gorm.DB) middleware.AppPasswordVerify {
	repo := repositories.NewCredentialRepository(db)
	return func(address, password string) (*auth.Claims, error) {
		now := time.Now()
		cred, mailbox, err := repo.VerifyAppPassword(address, password, now)
		if err != nil {
			return nil, err
		}
		account, err := webmailAccountFor(db, mailbox.ID)
		if err != nil {
			return nil, err
		}
		if cred.LastUsedAt == nil || now.Sub(*cred.LastUsedAt) >= repositories.LastUsedEvery {
			if err := repo.TouchLastUsed(cred.ID, now); err != nil {
				slog.Warn("could not record an app password's use", "credential_id", cred.ID, "error", err)
			}
		}
		return &auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: fmt.Sprintf("mailbox:%d", mailbox.ID)},
			Email:            mailbox.Address,
			WebmailAccountID: account.ID,
			MailboxID:        mailbox.ID,
			UserType:         "mailbox",
			TokenType:        "access",
			CredentialID:     cred.ID,
		}, nil
	}
}

// webmailAccountFor finds the webmail account of a mailbox, creating it as a
// first sign-in does when there is none.
func webmailAccountFor(db *gorm.DB, mailboxID uint) (*models.WebmailAccount, error) {
	var account models.WebmailAccount
	err := db.Where("primary_mailbox_id = ?", mailboxID).Take(&account).Error
	if err == nil {
		return &account, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	account = models.WebmailAccount{PrimaryMailboxID: mailboxID}
	if err := db.Create(&account).Error; err != nil {
		// Another request may have just made it: primary_mailbox_id is unique.
		if err := db.Where("primary_mailbox_id = ?", mailboxID).Take(&account).Error; err != nil {
			return nil, err
		}
	}
	return &account, nil
}
