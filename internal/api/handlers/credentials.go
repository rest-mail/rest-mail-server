package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	apitypes "github.com/restmail/restmail/internal/api/types"
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

// forRequest gives SQL work the HTTP budget without changing the shared handler.
// Keep an injected in-memory two-factor store; the production repository uses
// the same scoped connection as password checks, grants and credential writes.
func (h *CredentialHandler) forRequest(r *http.Request) *CredentialHandler {
	if h.db == nil {
		return h
	}
	scoped := *h
	scoped.db = h.db.WithContext(r.Context())
	scoped.repo = repositories.NewCredentialRepository(scoped.db)
	if _, ok := h.twoFactor.(*repositories.TwoFactorRepository); ok {
		scoped.twoFactor = repositories.NewTwoFactorRepository(scoped.db)
	}
	return &scoped
}

// Register registers an account's app password and API key routes, which need a signed-in
// person. checks are login's throttle, for the routes that check the current password, and
// the audit log.
func (h *CredentialHandler) Register(rs endpoint.Routes, checks SessionRouteChecks) {
	h.registerClientSecrets(rs, checks)
	tags := []string{"Credentials"}
	refusals := []int{http.StatusForbidden, http.StatusNotFound}
	manage := handlerChecked(models.CapMailboxManage, "CookieAuth", "BearerAuth")
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/credentials",
		OperationID: "listCredentials", Summary: "List an account's app passwords", Tags: tags,
		Errors: refusals, Security: manage}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/credentials",
		OperationID: "createAppPassword", Summary: "Make an app password", Tags: tags,
		Status: http.StatusCreated, Errors: refusals, Security: manage,
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("credentials.create", middleware.AuditBodyFields("name"),
			middleware.AuditResponseField("id", "credential_id"), middleware.AuditResponseField("public_id", "public_id"))}}, h.create)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/accounts/{id}/credentials/{credential_id}",
		OperationID: "revokeCredential", Summary: "Revoke an app password", Tags: tags,
		Status: http.StatusNoContent, Errors: refusals, Security: manage,
		Middleware: []endpoint.Middleware{checks.Audit(middleware.AuditActionCredentialsRevoke)}}, h.revoke)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/api-keys",
		OperationID: "listAPIKeys", Summary: "List an account's API keys", Tags: tags,
		Errors: refusals, Security: manage, Middleware: []endpoint.Middleware{noStore}}, h.listAPIKeys)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/api-keys",
		OperationID: "createAPIKey", Summary: "Make a scoped API key", Tags: tags,
		Status: http.StatusCreated, Errors: refusals, Security: manage,
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit(ActionAPIKeyCreate, middleware.AuditFailuresOnly()), noStore}}, h.createAPIKey)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/accounts/{id}/api-keys/{credential_id}",
		OperationID: "revokeAPIKey", Summary: "Revoke an API key", Tags: tags,
		Status: http.StatusNoContent, Errors: refusals, Security: manage,
		Middleware: []endpoint.Middleware{checks.Audit(ActionAPIKeyRevoke, middleware.AuditFailuresOnly())}}, h.revokeAPIKey)
}

// noStore keeps every answer of a route out of caches, refusals and binding's among them:
// about an account's API keys (the list, and the one answer that carries a key's secret), and
// RESTMAIL discovery, which a sender must not remember, nor a failure as if it were one.
var noStore = endpoint.Middleware{Wrap: func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}}

// credentialPath names one of an account's credentials by the path's {id} and
// {credential_id}.
type credentialPath struct {
	ID           uint32 `path:"id" minimum:"1" example:"1"`
	CredentialID uint32 `path:"credential_id" minimum:"1" example:"1"`
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
// such as an automation's account with no password of its own). It refuses
// any other; an account the session cannot manage is answered as one that
// does not exist.
func (h *CredentialHandler) managedAccount(r *http.Request, id uint32) (*auth.Claims, uint, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return nil, 0, errAuthenticationRequired
	}
	// The routes take no credential over HTTP Basic, but a credential must
	// never manage credentials, whatever the route says.
	if claims.CredentialID != 0 {
		return nil, 0, endpoint.Refuse(http.StatusForbidden, "forbidden", "Sign in with your password to manage app passwords")
	}
	if _, err := mayReach(h.db, claims, uint(id), models.CapMailboxManage); err != nil {
		if errors.Is(err, errNoAccess) {
			return nil, 0, errAccountNotFound
		}
		return nil, 0, fmt.Errorf("read the account: %w", err)
	}
	return claims, uint(id), nil
}

// list returns the mailbox's app passwords that are not revoked.
// GET /api/v1/accounts/{id}/credentials
func (h *CredentialHandler) list(r *http.Request, in accountPath) (endpoint.List[credentialResponse], error) {
	h = h.forRequest(r)
	_, accountID, err := h.managedAccount(r, in.ID)
	if err != nil {
		return endpoint.List[credentialResponse]{}, err
	}
	creds, err := h.repo.ListForAccount(accountID, models.CredentialKindAppPassword)
	if err != nil {
		return endpoint.List[credentialResponse]{}, fmt.Errorf("list credentials: %w", err)
	}
	out := make([]credentialResponse, len(creds))
	for i := range creds {
		out[i] = toCredentialResponse(&creds[i])
	}
	return endpoint.List[credentialResponse]{Data: out}, nil
}

// createCredentialBody is what the mobile app sends: a name, the session's current password
// and, optionally, when the app password expires. A missing password is the 403
// reauth_required the handler answers, not a validation failure.
type createCredentialBody struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Password is the mailbox's current password, entered again. It is
	// checked and never stored, logged or answered with.
	Password string `json:"password,omitempty"`
}

type createCredentialRequest struct {
	ID   uint32               `path:"id" minimum:"1" example:"1"`
	Body createCredentialBody `body:""`
}

// create makes an app password and answers with it, once.
// POST /api/v1/accounts/{id}/credentials
func (h *CredentialHandler) create(r *http.Request, in createCredentialRequest) (createdAppPassword, error) {
	h = h.forRequest(r)
	claims, accountID, err := h.managedAccount(r, in.ID)
	if err != nil {
		return createdAppPassword{}, err
	}
	req := in.Body
	req.Name = strings.TrimSpace(req.Name)
	now := h.now()
	switch {
	case req.Name == "":
		return createdAppPassword{}, endpoint.Invalid(map[string]string{"name": "required"})
	case utf8.RuneCountInString(req.Name) > maxCredentialNameLength:
		return createdAppPassword{}, endpoint.Invalid(map[string]string{"name": fmt.Sprintf("must be at most %d characters", maxCredentialNameLength)})
	case strings.IndexFunc(req.Name, unicode.IsControl) >= 0:
		return createdAppPassword{}, endpoint.Invalid(map[string]string{"name": "must not contain control characters"})
	case req.ExpiresAt != nil && !req.ExpiresAt.After(now):
		return createdAppPassword{}, endpoint.Invalid(map[string]string{"expires_at": "must be in the future"})
	}

	// Creating a credential needs a person who knows the mailbox's password
	// now. A session outlives a change of password by up to its access token's
	// lifetime, and an app password made in that window, by whoever learnt the
	// old password, would outlive the change.
	if err := h.checkCurrentPassword(claims.AccountID, req.Password); err != nil {
		return createdAppPassword{}, err
	}

	// It also needs a session whose sign-in took the mailbox's second factor,
	// when the mailbox has one.
	_, err = h.twoFactor.GetActive(claims.AccountID)
	switch {
	case err == nil && !claims.TwoFactor:
		return createdAppPassword{}, endpoint.Refuse(http.StatusForbidden, "two_factor_required",
			"This mailbox has two-factor authentication: sign in again with your code to create an app password")
	case err != nil && !errors.Is(err, repositories.ErrTwoFactorNotFound):
		return createdAppPassword{}, fmt.Errorf("verify two-factor authentication: %w", err)
	}

	cred, password, err := h.repo.CreateAppPassword(accountID, claims.AccountID, req.Name, req.ExpiresAt)
	if err != nil {
		slog.Error("creating an app password failed", "account_id", accountID, "error", err)
		return createdAppPassword{}, fmt.Errorf("create the app password: %w", err)
	}
	return createdAppPassword{credentialResponse: toCredentialResponse(cred), Password: password}, nil
}

// checkCurrentPassword checks password against the mailbox's password hash
// as it is now, with the same bcrypt comparison as signing in. A mailbox that
// is gone or disabled is compared against a dummy hash, so it costs the same,
// and refused. It refuses unless password matches.
// A refusal is 403, never 401: the session is still good (#392).
func (h *CredentialHandler) checkCurrentPassword(accountID uint, password string) error {
	if password == "" {
		return endpoint.Refuse(http.StatusForbidden, "reauth_required",
			"Enter your current password to create a credential")
	}
	var account models.Mailbox
	err := h.db.Select("id").Where("id = ? AND active = ?", accountID, true).Take(&account).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("verify the password: %w", err)
	}
	cred, matchErr := h.repo.MatchPassword(account.ID, password, h.now())
	if matchErr != nil {
		return fmt.Errorf("verify the password: %w", matchErr)
	}
	if cred == nil || err != nil {
		return endpoint.Refuse(http.StatusForbidden, "invalid_password", "The password is incorrect")
	}
	return nil
}

// revoke revokes one of the mailbox's app passwords. Its row stays, revoked,
// for the audit trail; the next request made with it is refused.
// DELETE /api/v1/accounts/{id}/credentials/{credential_id}
func (h *CredentialHandler) revoke(r *http.Request, in credentialPath) (endpoint.Empty, error) {
	h = h.forRequest(r)
	_, accountID, err := h.managedAccount(r, in.ID)
	if err != nil {
		return endpoint.Empty{}, err
	}
	err = h.repo.Revoke(accountID, uint(in.CredentialID), h.now())
	if errors.Is(err, repositories.ErrCredentialNotFound) {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Credential not found")
	}
	if err != nil {
		return endpoint.Empty{}, fmt.Errorf("revoke the credential: %w", err)
	}
	return endpoint.Empty{}, nil
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

type protocolSignInRequest = apitypes.ProtocolSignInRequest

// ProtocolSignedIn is the account a mail client signed in to, and the credential it used.
// Its fields are in the order the map it replaced was written in.
type ProtocolSignedIn = apitypes.ProtocolSignedIn

// RegisterProtocolSignIn registers the protocol gateways' sign-in, in the mail routes' group:
// it takes HTTP Basic only (a session is 400 basic_required), is throttled like login, and is
// audited with its protocol.
func (h *CredentialHandler) RegisterProtocolSignIn(rs endpoint.Routes, checks SessionRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/auth/protocol-sign-in",
		OperationID: "protocolSignIn", Summary: "Sign a mail client in (protocol gateways)", Tags: []string{"Auth"},
		Security:   []oapifly.SecurityRequirement{{"BasicAuth": nil}},
		Middleware: []endpoint.Middleware{checks.Throttle, checks.Audit("auth.protocol_sign_in", middleware.AuditBodyFields("protocol"))}}, h.protocolSignIn)
}

// protocolSignIn is how a protocol gateway signs in a mail client: the request
// carries the address and what the client gave as its password (an app
// password, or one of the account's passwords) with HTTP Basic, already
// checked by the middleware against the account's credentials, and the audit
// log records the sign-in with its credential. It answers with the account,
// as login does, so the gateway knows which account the requests it then
// makes, each carrying the same Basic credentials, are for. A session cannot
// use it.
// POST /api/v1/auth/protocol-sign-in
func (h *CredentialHandler) protocolSignIn(r *http.Request, req protocolSignInRequest) (ProtocolSignedIn, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	if claims == nil || claims.CredentialID == 0 || claims.IsScopedCredential() {
		return ProtocolSignedIn{}, endpoint.Refuse(http.StatusBadRequest, "basic_required",
			"Sign in here with HTTP Basic: the account's address, and its password or an app password")
	}
	if !signInProtocols[req.Protocol] {
		return ProtocolSignedIn{}, endpoint.Invalid(map[string]string{"protocol": `must be "imap", "pop3" or "smtp"`})
	}
	var account models.Mailbox
	if err := h.db.Select("id", "address", "display_name").Take(&account, claims.AccountID).Error; err != nil {
		return ProtocolSignedIn{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to load the account")
	}
	return ProtocolSignedIn{
		CredentialID:   claims.CredentialID,
		CredentialKind: claims.CredentialKind,
		User: userInfo{
			ID:          account.ID,
			Email:       account.Address,
			DisplayName: account.DisplayName,
		},
	}, nil
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
