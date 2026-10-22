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

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
)

// AccountHandler serves the accounts a session reaches: its own, and every
// account it holds a mail grant on (#347). An account is a mailbox; "sub
// account" is only a relationship, an account another one holds the owner
// grant on, so there is no separate API for it.
type AccountHandler struct {
	db *gorm.DB
	// cache is the grants cache RequireCapability reads; every grant made
	// here empties it.
	cache *grants.Cache
	// masterKey decrypts TOTP secrets, for linking an account with 2FA.
	masterKey string
}

func NewAccountHandler(db *gorm.DB, cache *grants.Cache, masterKey string) *AccountHandler {
	return &AccountHandler{db: db, cache: cache, masterKey: masterKey}
}

// accountResponse is an account as GET /api/v1/accounts lists it. ID is the
// mailbox's id, the {id} of every /accounts/{id}/… route. IsPrimary marks the
// session's own account.
type accountResponse struct {
	ID          uint   `json:"id"`
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	IsPrimary   bool   `json:"is_primary"`
}

func toAccountResponse(mb *models.Mailbox, ownID uint) accountResponse {
	return accountResponse{ID: mb.ID, Address: mb.Address, DisplayName: mb.DisplayName, IsPrimary: mb.ID == ownID}
}

// mailCapabilities are the capabilities that make a grant on a mailbox a mail
// grant: one that lists the mailbox among the holder's accounts.
var mailCapabilities = []string{models.CapMailRead, models.CapMailWrite, models.CapMailSend, grants.Wildcard}

// ListAccounts lists the session's own account, first, then every account it
// holds an active mail grant on at that account's own scope (an account it
// created or linked), by address. Domain and server-wide admin grants reach
// mailboxes too, but they are administration, not accounts of its own, so
// they do not list them. A request made with an app password lists only its
// own account.
// GET /api/v1/accounts
func (h *AccountHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	var own models.Mailbox
	if err := h.db.First(&own, claims.AccountID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Account not found")
		return
	}
	accounts := []accountResponse{toAccountResponse(&own, own.ID)}
	if !claims.MailboxOnly() {
		var others []models.Mailbox
		err := h.db.Model(&models.Mailbox{}).Distinct("mailboxes.*").
			Joins("JOIN grants g ON g.scope = 'mailbox:' || lower(mailboxes.address)").
			Joins("JOIN role_capabilities rc ON rc.role_id = g.role_id").
			Joins("JOIN capabilities c ON c.id = rc.capability_id").
			Where("g.holder_kind = ? AND g.holder_id = ? AND g.mode = ?", models.GrantHolderAccount, claims.AccountID, models.GrantActive).
			Where("g.expires_at IS NULL OR g.expires_at > ?", time.Now()).
			Where("c.name IN ?", mailCapabilities).
			Where("mailboxes.id <> ?", own.ID).
			Order("mailboxes.address").
			Find(&others).Error
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list accounts")
			return
		}
		for i := range others {
			accounts = append(accounts, toAccountResponse(&others[i], own.ID))
		}
	}
	if claims.IsAPIKey() {
		visible := make([]accountResponse, 0, len(accounts))
		for _, account := range accounts {
			if !middleware.CredentialAllows(claims, models.CapMailRead, grants.Mailbox(account.Address)) {
				continue
			}
			if _, err := mayReach(h.db, claims, account.ID, models.CapMailRead); err == nil {
				visible = append(visible, account)
			} else if !errors.Is(err, errNoAccess) {
				respond.Error(w, http.StatusServiceUnavailable, "unavailable", "Could not verify permissions")
				return
			}
		}
		accounts = visible
	}
	respond.List(w, accounts, nil)
}

// GetAccount returns one account the session may read.
// GET /api/v1/accounts/{id}
func (h *AccountHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}
	mb, err := mayReach(h.db, claims, uint(id), models.CapMailRead)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Account not found")
		return
	}
	respond.Data(w, http.StatusOK, toAccountResponse(mb, claims.AccountID))
}

type createAccountRequest struct {
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	// Password, when given, becomes the account's password credential, so it
	// can be signed in to. Without one the account has no credentials, and is
	// reached only through its owners.
	Password string `json:"password"`
}

// ActionAccountsCreate is the audit action of a created account.
const ActionAccountsCreate = "accounts.create"

// CreateAccount creates an account, the same for everyone: it needs
// mailboxes:write covering the address's domain, and the creator gets the
// owner grant on it. With a password the account gets a password credential;
// without one it has none. The account, its credential, the grant and their
// audit entries commit together.
// POST /api/v1/accounts {"address", "display_name"?, "password"?}
func (h *AccountHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	var req createAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	address, addressErr := rmail.CanonicalMailboxAddress(req.Address)
	local, domainName, _ := strings.Cut(address, "@")
	problems := map[string]string{}
	if strings.TrimSpace(req.Address) == "" {
		problems["address"] = "required"
	} else if addressErr != nil {
		problems["address"] = "must be a mail address: local@domain"
	} else if _, err := grants.ParseScope(grants.Mailbox(address)); err != nil {
		problems["address"] = "must be a mail address: local@domain"
	}
	if req.Password != "" && len(req.Password) < minChosenPasswordLength {
		problems["password"] = fmt.Sprintf("must be at least %d characters", minChosenPasswordLength)
	}
	if len(problems) > 0 {
		respond.ValidationError(w, problems)
		return
	}

	held, err := grants.Load(h.db, claims.AccountID, time.Now())
	if err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "unavailable", "Could not verify permissions")
		return
	}
	if !held.Allows(middleware.CapMailboxesWrite, grants.Domain(domainName)) ||
		!middleware.CredentialAllows(claims, middleware.CapMailboxesWrite, grants.Domain(domainName)) {
		respond.Error(w, http.StatusForbidden, "forbidden", "You cannot create accounts on "+domainName)
		return
	}

	var domain models.Domain
	if err := h.db.Where("name = ?", domainName).First(&domain).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respond.ValidationError(w, map[string]string{"address": "this server does not host " + domainName})
			return
		}
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to read the domain")
		return
	}

	var hash string
	if req.Password != "" {
		if hash, err = hashPassword(req.Password); err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to set the password")
			return
		}
	}

	var owner models.Role
	if err := h.db.Where("name = ?", models.RoleOwner).First(&owner).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "The owner role is missing")
		return
	}

	account := models.Mailbox{
		DomainID:    domain.ID,
		LocalPart:   local,
		Address:     address,
		DisplayName: strings.TrimSpace(req.DisplayName),
		QuotaBytes:  domain.DefaultQuotaBytes,
		Active:      true,
	}
	errExists := errors.New("exists")
	err = h.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		// Earlier versions could store decomposed local parts. Do not create a
		// second identity equivalent to one of those existing accounts.
		if err := tx.Model(&models.Mailbox{}).Where("domain_id = ? AND normalize(address, NFC) = ?", domain.ID, address).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errExists
		}
		if err := tx.Create(&account).Error; err != nil {
			if repositories.IsUniqueViolation(err) {
				return errExists
			}
			return err
		}
		if err := tx.Create(&models.QuotaUsage{MailboxID: account.ID}).Error; err != nil {
			return err
		}
		now := time.Now()
		if hash != "" {
			creator := claims.AccountID
			if _, err := repositories.NewCredentialRepository(tx).SetPassword(account.ID, hash, &creator, now); err != nil {
				return err
			}
		}
		e := middleware.NewAuditEntry(ActionAccountsCreate, r, now)
		e.Status = http.StatusCreated
		e.Target["account_id"] = strconv.FormatUint(uint64(account.ID), 10)
		e.Target["address"] = account.Address
		e.Target["password"] = strconv.FormatBool(hash != "")
		if err := audit.Append(tx, e); err != nil {
			return err
		}
		return grantOwner(tx, r, claims, &account, owner)
	})
	switch {
	case errors.Is(err, errExists):
		respond.Error(w, http.StatusConflict, "already_exists", "An account with this address already exists")
		return
	case err != nil:
		slog.Error("creating an account failed", "address", address, "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create the account")
		return
	}
	h.cache.Invalidate()
	respond.Data(w, http.StatusCreated, toAccountResponse(&account, claims.AccountID))
}

// grantOwner gives the session's account the owner role on account, in tx,
// recorded as a grants.create entry. Creating or linking an account is what
// entitles it, so it needs no other check.
func grantOwner(tx *gorm.DB, r *http.Request, claims *auth.Claims, account *models.Mailbox, owner models.Role) error {
	granter := claims.AccountID
	g := &models.Grant{
		HolderKind:    models.GrantHolderAccount,
		HolderID:      claims.AccountID,
		RoleID:        owner.ID,
		Role:          owner,
		Scope:         grants.Mailbox(account.Address),
		Mode:          models.GrantActive,
		GrantedByKind: models.AuditActorAccount,
		GrantedByID:   &granter,
	}
	return grants.Add(tx, g, middleware.RequestEntry(r, grants.ActionCreate, http.StatusCreated))
}

type linkAccountRequest struct {
	Address      string `json:"address"`
	Password     string `json:"password"`
	DisplayName  string `json:"display_name"`
	TOTPCode     string `json:"totp_code"`
	RecoveryCode string `json:"recovery_code"`
}

// LinkAccount grants the session's account the owner role on an existing
// account, by proving one of its passwords, and its second factor when it has
// one: the same proof as signing in to it. Throttled like sign-in, and
// audited. display_name, when the account has none, becomes its name.
//
// A refused proof is 403, never 401: the secrets are the other account's, and
// the session that sent them is still good. A client that read 401 would
// renew that session and send the guess again (#385).
// POST /api/v1/accounts/link {"address", "password", "display_name"?}
func (h *AccountHandler) LinkAccount(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	var req linkAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Address) == "" || req.Password == "" {
		respond.ValidationError(w, map[string]string{
			"address":  "required",
			"password": "required",
		})
		return
	}

	// As at sign-in, a lookup miss does NOT short-circuit: MatchPassword runs one
	// bcrypt comparison against a dummy hash, so an unknown address, an account
	// with no password and a wrong password take the same time.
	var account models.Mailbox
	lookupErr := repositories.FindMailboxByAddress(h.db, req.Address, &account)
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to verify the password")
		return
	}
	cred, err := repositories.NewCredentialRepository(h.db).MatchPassword(account.ID, req.Password, time.Now())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to verify the password")
		return
	}
	if lookupErr != nil || cred == nil || !account.Active {
		respond.Error(w, http.StatusForbidden, "invalid_credentials", "Invalid email or password")
		return
	}
	// The password is proven; what follows may say why it is not enough.
	if account.PasswordChangeRequired || account.BootstrapExpired(time.Now()) {
		respond.Error(w, http.StatusForbidden, "setup_required", "That account must first sign in and choose its own password")
		return
	}
	tf := repositories.NewTwoFactorRepository(h.db)
	if _, err := tf.GetActive(account.ID); err == nil {
		if !verifyTOTPOrRecovery(tf, h.masterKey, account.ID, req.TOTPCode, req.RecoveryCode) {
			if req.TOTPCode == "" && req.RecoveryCode == "" {
				respond.Error(w, http.StatusForbidden, "totp_required", "That account has two-factor authentication: send its code too")
			} else {
				respond.Error(w, http.StatusForbidden, "invalid_totp", "Invalid two-factor authentication code")
			}
			return
		}
	} else if !errors.Is(err, repositories.ErrTwoFactorNotFound) {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to verify two-factor authentication")
		return
	}
	if account.ID == claims.AccountID {
		respond.Error(w, http.StatusConflict, "already_exists", "This is your own account")
		return
	}

	var owner models.Role
	if err := h.db.Where("name = ?", models.RoleOwner).First(&owner).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "The owner role is missing")
		return
	}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if name := strings.TrimSpace(req.DisplayName); name != "" && account.DisplayName == "" {
			if err := tx.Model(&account).Update("display_name", name).Error; err != nil {
				return err
			}
		}
		return grantOwner(tx, r, claims, &account, owner)
	})
	switch {
	case errors.Is(err, grants.ErrExists):
		respond.Error(w, http.StatusConflict, "already_exists", "This account is already linked")
		return
	case err != nil:
		slog.Error("linking an account failed", "account_id", account.ID, "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to link the account")
		return
	}
	h.cache.Invalidate()
	respond.Data(w, http.StatusCreated, toAccountResponse(&account, claims.AccountID))
}

// UnlinkAccount removes the session's own grants on another account: giving
// up access needs nothing more. Each removal is recorded as a grants.delete
// entry, in the same transaction. Its own account cannot be unlinked.
// DELETE /api/v1/accounts/{id}
func (h *AccountHandler) UnlinkAccount(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}
	if uint(id) == claims.AccountID {
		respond.Error(w, http.StatusConflict, "own_account", "You cannot unlink your own account")
		return
	}
	var account models.Mailbox
	if err := h.db.First(&account, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Linked account not found")
		return
	}
	errNoGrant := errors.New("no grant")
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		var held []models.Grant
		if err := tx.Preload("Role").
			Where("holder_kind = ? AND holder_id = ? AND scope = ?", models.GrantHolderAccount, claims.AccountID, grants.Mailbox(account.Address)).
			Order("id").Find(&held).Error; err != nil {
			return err
		}
		if len(held) == 0 {
			return errNoGrant
		}
		for i := range held {
			if err := grants.Remove(tx, &held[i], middleware.RequestEntry(r, grants.ActionDelete, http.StatusNoContent)); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errNoGrant):
		respond.Error(w, http.StatusNotFound, "not_found", "Linked account not found")
		return
	case err != nil:
		slog.Error("unlinking an account failed", "account_id", account.ID, "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to unlink the account")
		return
	}
	h.cache.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}
