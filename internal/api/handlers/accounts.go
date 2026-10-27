package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	apitypes "github.com/restmail/restmail/internal/api/types"

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

// forRequest gives the handler's SQL work the HTTP request's budget and
// cancellation without changing the shared handler: reads, access checks,
// password and second-factor checks and the account transactions run on the
// scoped connection.
func (h *AccountHandler) forRequest(r *http.Request) *AccountHandler {
	if h.db == nil {
		return h
	}
	scoped := *h
	scoped.db = h.db.WithContext(r.Context())
	return &scoped
}

// SessionRouteChecks are the middleware routes.go makes for the account and credential
// routes a signed-in person reaches: login's per-client-IP throttle, for a route that checks
// a password, and the audit log.
type SessionRouteChecks struct {
	Throttle endpoint.Middleware
	Audit    func(action string, options ...middleware.AuditOption) endpoint.Middleware
}

// RegisterReads registers the account reads, which the mail routes' group serves: a session,
// a scoped API key or HTTP Basic.
func (h *AccountHandler) RegisterReads(rs endpoint.Routes) {
	tags := []string{"Accounts"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts",
		OperationID: "listAccounts", Summary: "List the accounts this one reaches", Tags: tags,
		Errors: []int{http.StatusNotFound}}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}",
		OperationID: "getAccount", Summary: "Get an account this one may read", Tags: tags,
		Errors:   []int{http.StatusNotFound},
		Security: handlerChecked(models.CapMailRead, "CookieAuth", "BearerAuth", "APIKeyAuth", "BasicAuth")}, h.get)
}

// RegisterCreate registers creating an account, behind audit. Its routes take a session or a
// scoped API key, and the handler checks mailboxes:write on the address's domain.
func (h *AccountHandler) RegisterCreate(rs endpoint.Routes, audit endpoint.Middleware) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts",
		OperationID: "createAccount", Summary: "Create an account", Tags: []string{"Accounts"},
		Status: http.StatusCreated, Errors: []int{http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable},
		Security:   handlerChecked(middleware.CapMailboxesWrite, "CookieAuth", "BearerAuth", "APIKeyAuth"),
		Middleware: []endpoint.Middleware{audit}}, h.create)
}

// RegisterLinks registers linking and unlinking an account, which need a signed-in person.
func (h *AccountHandler) RegisterLinks(rs endpoint.Routes, checks SessionRouteChecks) {
	tags := []string{"Accounts"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/link",
		OperationID: "linkAccount", Summary: "Link an existing account by proving its password", Tags: tags,
		Status: http.StatusCreated, Errors: []int{http.StatusForbidden, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.Throttle,
			checks.Audit("accounts.link", middleware.AuditBodyFields("address"))}}, h.link)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/accounts/{id}",
		OperationID: "unlinkAccount", Summary: "Give up access to an account", Tags: tags,
		Description: "Missing links retain 404; account lookup and grant-removal failures return 500. " +
			"The lookup and removal transaction follow request cancellation.",
		Status: http.StatusNoContent, Errors: []int{http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.Audit("accounts.unlink")}}, h.unlink)
}

// handlerChecked states the capability a handler checks at the account it acts on, held on
// each scheme its route takes: the route's middleware authenticates, the handler authorises.
func handlerChecked(capability string, schemes ...string) []oapifly.SecurityRequirement {
	out := make([]oapifly.SecurityRequirement, len(schemes))
	for i, scheme := range schemes {
		out[i] = oapifly.SecurityRequirement{scheme: {capability}}
	}
	return out
}

// accountPath names an account by the path's {id}, the mailbox's id.
type accountPath struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}

var errAuthenticationRequired = endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")

// accountResponse is an account as GET /api/v1/accounts lists it. ID is the
// mailbox's id, the {id} of every /accounts/{id}/… route. IsPrimary marks the
// session's own account.
type accountResponse = apitypes.AccountResponse

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
func (h *AccountHandler) list(r *http.Request, _ struct{}) (endpoint.List[accountResponse], error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	if claims == nil {
		return endpoint.List[accountResponse]{}, errAuthenticationRequired
	}
	var own models.Mailbox
	if err := h.db.First(&own, claims.AccountID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.List[accountResponse]{}, errAccountNotFound
		}
		return endpoint.List[accountResponse]{}, fmt.Errorf("read the primary account: %w", err)
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
			return endpoint.List[accountResponse]{}, fmt.Errorf("list accounts: %w", err)
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
				return endpoint.List[accountResponse]{}, errPermissionsUnverified
			}
		}
		accounts = visible
	}
	return endpoint.List[accountResponse]{Data: accounts}, nil
}

var (
	errAccountNotFound       = endpoint.Refuse(http.StatusNotFound, "not_found", "Account not found")
	errPermissionsUnverified = endpoint.Refuse(http.StatusServiceUnavailable, "unavailable", "Could not verify permissions")
)

// GetAccount returns one account the session may read.
// GET /api/v1/accounts/{id}
func (h *AccountHandler) get(r *http.Request, in accountPath) (accountResponse, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	if claims == nil {
		return accountResponse{}, errAuthenticationRequired
	}
	mb, err := mayReach(h.db, claims, uint(in.ID), models.CapMailRead)
	if err != nil {
		if errors.Is(err, errNoAccess) {
			return accountResponse{}, errAccountNotFound
		}
		return accountResponse{}, err
	}
	return toAccountResponse(mb, claims.AccountID), nil
}

// createAccountRequest is what the mobile app and the admin webmail send: an address, and
// optionally a display name and a password. Each is checked in the handler, as before:
// a blank or empty password means none.
type createAccountRequest = apitypes.CreateAccountRequest

// ActionAccountsCreate is the audit action of a created account.
const ActionAccountsCreate = "accounts.create"

// CreateAccount creates an account, the same for everyone: it needs
// mailboxes:write covering the address's domain, and the creator gets the
// owner grant on it. With a password the account gets a password credential;
// without one it has none. The account, its credential, the grant and their
// audit entries commit together.
// POST /api/v1/accounts {"address", "display_name"?, "password"?}
func (h *AccountHandler) create(r *http.Request, req createAccountRequest) (accountResponse, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	if claims == nil {
		return accountResponse{}, errAuthenticationRequired
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
	var importedHash string
	if req.PasswordHash != "" {
		if req.Password != "" {
			problems["password_hash"] = "send password or password_hash, not both"
		} else {
			var err error
			importedHash, err = auth.NormalizeImportedPasswordHash(req.PasswordHash)
			if err != nil {
				problems["password_hash"] = err.Error()
			}
		}
	}
	if len(problems) > 0 {
		return accountResponse{}, endpoint.Invalid(problems)
	}

	held, err := grants.Load(h.db, claims.AccountID, time.Now())
	if err != nil {
		return accountResponse{}, errPermissionsUnverified
	}
	if !held.Allows(middleware.CapMailboxesWrite, grants.Domain(domainName)) ||
		!middleware.CredentialAllows(claims, middleware.CapMailboxesWrite, grants.Domain(domainName)) {
		return accountResponse{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "You cannot create accounts on "+domainName)
	}

	var domain models.Domain
	if err := h.db.Where("name = ?", domainName).First(&domain).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return accountResponse{}, endpoint.Invalid(map[string]string{"address": "this server does not host " + domainName})
		}
		return accountResponse{}, fmt.Errorf("read the domain: %w", err)
	}

	hash := importedHash
	if req.Password != "" {
		if hash, err = hashPassword(req.Password); err != nil {
			return accountResponse{}, fmt.Errorf("set the password: %w", err)
		}
	}

	var owner models.Role
	if err := h.db.Where("name = ?", models.RoleOwner).First(&owner).Error; err != nil {
		return accountResponse{}, fmt.Errorf("the owner role is missing: %w", err)
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
		return accountResponse{}, endpoint.Refuse(http.StatusConflict, "already_exists", "An account with this address already exists")
	case err != nil:
		slog.Error("creating an account failed", "address", address, "error", err)
		return accountResponse{}, fmt.Errorf("create the account: %w", err)
	}
	h.cache.Invalidate()
	return toAccountResponse(&account, claims.AccountID), nil
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

// linkAccountRequest is what the mobile app and webmail send. The webmail sends display_name
// always, empty when the user gave none. The handler checks address and password together, so
// one missing names both, as the documented examples show.
type linkAccountRequest struct {
	Address      string `json:"address" minLength:"1" pattern:"\\S" message:"required" missing:"required"`
	Password     string `json:"password" minLength:"1" message:"required" missing:"required"`
	DisplayName  string `json:"display_name,omitempty"`
	TOTPCode     string `json:"totp_code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
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
func (h *AccountHandler) link(r *http.Request, req linkAccountRequest) (accountResponse, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	if claims == nil {
		return accountResponse{}, errAuthenticationRequired
	}
	// As at sign-in, a lookup miss does NOT short-circuit: MatchPassword runs one
	// bcrypt comparison against a dummy hash, so an unknown address, an account
	// with no password and a wrong password take the same time.
	var account models.Mailbox
	lookupErr := repositories.FindMailboxByAddress(h.db, req.Address, &account)
	if lookupErr != nil && !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return accountResponse{}, fmt.Errorf("verify the password: %w", lookupErr)
	}
	cred, err := repositories.NewCredentialRepository(h.db).MatchPassword(account.ID, req.Password, time.Now())
	if err != nil {
		return accountResponse{}, fmt.Errorf("verify the password: %w", err)
	}
	if lookupErr != nil || cred == nil || !account.Active {
		return accountResponse{}, endpoint.Refuse(http.StatusForbidden, "invalid_credentials", "Invalid email or password")
	}
	// The password is proven; what follows may say why it is not enough.
	if account.PasswordChangeRequired || account.BootstrapExpired(time.Now()) {
		return accountResponse{}, endpoint.Refuse(http.StatusForbidden, "setup_required", "That account must first sign in and choose its own password")
	}
	tf := repositories.NewTwoFactorRepository(h.db)
	if _, err := tf.GetActive(account.ID); err == nil {
		verified, err := verifyTOTPOrRecovery(tf, h.masterKey, account.ID, req.TOTPCode, req.RecoveryCode)
		if err != nil {
			return accountResponse{}, fmt.Errorf("verify two-factor authentication: %w", err)
		}
		if !verified {
			if req.TOTPCode == "" && req.RecoveryCode == "" {
				return accountResponse{}, endpoint.Refuse(http.StatusForbidden, "totp_required", "That account has two-factor authentication: send its code too")
			}
			return accountResponse{}, endpoint.Refuse(http.StatusForbidden, "invalid_totp", "Invalid two-factor authentication code")
		}
	} else if !errors.Is(err, repositories.ErrTwoFactorNotFound) {
		return accountResponse{}, fmt.Errorf("verify two-factor authentication: %w", err)
	}
	if account.ID == claims.AccountID {
		return accountResponse{}, endpoint.Refuse(http.StatusConflict, "already_exists", "This is your own account")
	}

	var owner models.Role
	if err := h.db.Where("name = ?", models.RoleOwner).First(&owner).Error; err != nil {
		return accountResponse{}, fmt.Errorf("the owner role is missing: %w", err)
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
		return accountResponse{}, endpoint.Refuse(http.StatusConflict, "already_exists", "This account is already linked")
	case err != nil:
		slog.Error("linking an account failed", "account_id", account.ID, "error", err)
		return accountResponse{}, fmt.Errorf("link the account: %w", err)
	}
	h.cache.Invalidate()
	return toAccountResponse(&account, claims.AccountID), nil
}

// UnlinkAccount removes the session's own grants on another account: giving
// up access needs nothing more. Each removal is recorded as a grants.delete
// entry, in the same transaction. Its own account cannot be unlinked.
// DELETE /api/v1/accounts/{id}
func (h *AccountHandler) unlink(r *http.Request, in accountPath) (endpoint.Empty, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return endpoint.Empty{}, errAuthenticationRequired
	}
	if uint(in.ID) == claims.AccountID {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusConflict, "own_account", "You cannot unlink your own account")
	}
	errLinkNotFound := endpoint.Refuse(http.StatusNotFound, "not_found", "Linked account not found")
	var account models.Mailbox
	if err := h.db.WithContext(r.Context()).First(&account, in.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Empty{}, errLinkNotFound
		}
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to look up linked account")
	}
	errNoGrant := errors.New("no grant")
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
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
		return endpoint.Empty{}, errLinkNotFound
	case err != nil:
		slog.Error("unlinking an account failed", "account_id", account.ID, "error", err)
		return endpoint.Empty{}, fmt.Errorf("unlink the account: %w", err)
	}
	h.cache.Invalidate()
	return endpoint.Empty{}, nil
}
