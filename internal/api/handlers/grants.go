package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// GrantHandler serves the granting API: list, grant, and remove grants (#345).
// A change needs a session that signed in with two-factor authentication, and
// the granter may only grant or remove a role whose capabilities it holds, at
// a scope within its own. Each change and its audit entry commit together.
//
// It also serves escalation (#346, escalation.go): an admin switching one of
// its own eligible grants on for a while.
type GrantHandler struct {
	db *gorm.DB
	// cache is the one RequireCapability reads; every change empties it.
	cache *grants.Cache
	// masterKey decrypts the TOTP secret a fresh code is checked against.
	masterKey string
}

func NewGrantHandler(db *gorm.DB, cache *grants.Cache, masterKey string) *GrantHandler {
	return &GrantHandler{db: db, cache: cache, masterKey: masterKey}
}

type grantHolder struct {
	Kind string `json:"kind"`
	ID   uint   `json:"id"`
}

type grantActor struct {
	Kind string `json:"kind"`
	ID   *uint  `json:"id"`
}

type grantResponse struct {
	ID        uint        `json:"id"`
	Holder    grantHolder `json:"holder"`
	Role      string      `json:"role"`
	RoleID    uint        `json:"role_id"`
	Scope     string      `json:"scope"`
	Mode      string      `json:"mode"`
	GrantedBy grantActor  `json:"granted_by"`
	GrantedAt string      `json:"granted_at"`
	ExpiresAt *string     `json:"expires_at"`
	// Activation is the one running now, of an eligible grant switched on.
	Activation *activationResponse `json:"activation"`
}

func toGrantResponse(g *models.Grant) grantResponse {
	return grantResponse{
		ID:        g.ID,
		Holder:    grantHolder{Kind: g.HolderKind, ID: g.HolderID},
		Role:      g.Role.Name,
		RoleID:    g.RoleID,
		Scope:     g.Scope,
		Mode:      g.Mode,
		GrantedBy: grantActor{Kind: g.GrantedByKind, ID: g.GrantedByID},
		GrantedAt: g.GrantedAt.UTC().Format(time.RFC3339),
		ExpiresAt: formatTime(g.ExpiresAt),
	}
}

// toGrantResponses is each grant, with the activation of it running now.
func toGrantResponses(db *gorm.DB, rows []models.Grant, now time.Time) ([]grantResponse, error) {
	ids := make([]uint, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	live, err := grants.LiveActivations(db, ids, now)
	if err != nil {
		return nil, err
	}
	out := make([]grantResponse, len(rows))
	for i := range rows {
		out[i] = toGrantResponse(&rows[i])
		if a, ok := live[rows[i].ID]; ok {
			out[i].Activation = toActivationResponse(&a, now)
		}
	}
	return out, nil
}

// List returns grants, oldest first, optionally only one holder's, or only
// those at one scope.
// GET /api/v1/admin/grants?holder_kind=account&holder_id=3&scope=mailbox:chris@example.com
func (h *GrantHandler) List(w http.ResponseWriter, r *http.Request) {
	if !validateIntegerQuery(w, r, 200, "limit", "offset") {
		return
	}
	q := h.db.Model(&models.Grant{})
	if kind := r.URL.Query().Get("holder_kind"); kind != "" {
		q = q.Where("holder_kind = ?", kind)
	}
	if v := r.URL.Query().Get("holder_id"); v != "" {
		id, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			respond.ValidationError(w, map[string]string{"holder_id": "must be a number"})
			return
		}
		q = q.Where("holder_id = ?", id)
	}
	if scope := r.URL.Query().Get("scope"); scope != "" {
		q = q.Where("scope = ?", strings.ToLower(strings.TrimSpace(scope)))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch grants")
		return
	}
	limit, offset := parsePagination(r, defaultListLimit, maxListLimit)
	var rows []models.Grant
	if err := q.Preload("Role").Order("id").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch grants")
		return
	}
	out, err := toGrantResponses(h.db, rows, time.Now())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch grants")
		return
	}
	respond.List(w, out, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}

type createGrantRequest struct {
	Holder    *grantHolder `json:"holder"`
	Role      string       `json:"role"`
	Scope     string       `json:"scope"`
	Mode      string       `json:"mode"`
	ExpiresAt *time.Time   `json:"expires_at"`
}

// Create grants a role at a scope.
// POST /api/v1/admin/grants {"holder":{"kind":"account","id":3},"role":"admin","scope":"domain:a.com","mode":"active"}
func (h *GrantHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if !requireTwoFactor(w, claims) {
		return
	}
	var req createGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	problems := map[string]string{}
	if req.Holder == nil || req.Holder.Kind == "" || req.Holder.ID == 0 {
		problems["holder"] = `required: {"kind": "account", "id": <account id>}`
	} else if req.Holder.Kind != models.GrantHolderAccount {
		problems["holder"] = `only accounts can be granted roles yet`
	}
	if req.Role == "" {
		problems["role"] = "required: a role name"
	}
	scope, err := grants.ParseScope(req.Scope)
	if err != nil {
		problems["scope"] = err.Error()
	}
	if req.Mode == "" {
		req.Mode = models.GrantActive
	}
	if req.Mode != models.GrantActive && req.Mode != models.GrantEligible {
		problems["mode"] = `must be "active" or "eligible"`
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		problems["expires_at"] = "must be in the future"
	}
	if len(problems) > 0 {
		respond.ValidationError(w, problems)
		return
	}

	g := &models.Grant{
		HolderKind: req.Holder.Kind,
		HolderID:   req.Holder.ID,
		Scope:      scope,
		Mode:       req.Mode,
		ExpiresAt:  req.ExpiresAt,
	}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		var account models.Mailbox
		if err := tx.Select("id").First(&account, g.HolderID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNoSuchHolder
			}
			return err
		}
		if err := tx.Where("name = ?", req.Role).First(&g.Role).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNoSuchRole
			}
			return err
		}
		g.RoleID = g.Role.ID
		return grantChange{r: r, claims: claims}.add(tx, g, http.StatusCreated)
	})
	if grantChangeFailed(w, err) {
		return
	}
	h.cache.Invalidate()
	respond.Data(w, http.StatusCreated, toGrantResponse(g))
}

// Delete removes a grant.
// DELETE /api/v1/admin/grants/{id}
func (h *GrantHandler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	id, err := pathID(r, "id")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_id", "Invalid grant ID")
		return
	}
	if !requireTwoFactor(w, claims) {
		return
	}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		var g models.Grant
		if err := tx.Preload("Role").First(&g, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errNoSuchGrant
			}
			return err
		}
		return grantChange{r: r, claims: claims}.remove(tx, &g, http.StatusNoContent)
	})
	if grantChangeFailed(w, err) {
		return
	}
	h.cache.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

var (
	errNoSuchHolder      = errors.New("no such holder")
	errNoSuchRole        = errors.New("no such role")
	errNoSuchGrant       = errors.New("no such grant")
	errTwoFactorRequired = errors.New("two-factor session required")
)

// requireTwoFactor refuses, and says so, a session that did not sign in with
// a second factor: changing grants needs a person (#341, Decided 5), and only a
// person's session can carry one.
func requireTwoFactor(w http.ResponseWriter, claims *auth.Claims) bool {
	if claims != nil && claims.HasAMR(auth.AMROTP) {
		return true
	}
	grantChangeFailed(w, errTwoFactorRequired)
	return false
}

// grantChange makes grant changes for the account a request comes from: each
// checked against what it holds, and recorded in the audit log in the same
// transaction.
type grantChange struct {
	r      *http.Request
	claims *auth.Claims
}

func (c grantChange) add(tx *gorm.DB, g *models.Grant, status int) error {
	if err := grants.Authorize(tx, c.claims.AccountID, g.RoleID, g.Scope); err != nil {
		return err
	}
	granter := c.claims.AccountID
	g.GrantedByKind, g.GrantedByID = models.AuditActorAccount, &granter
	return grants.Add(tx, g, middleware.RequestEntry(c.r, grants.ActionCreate, status))
}

func (c grantChange) remove(tx *gorm.DB, g *models.Grant, status int) error {
	if err := grants.Authorize(tx, c.claims.AccountID, g.RoleID, g.Scope); err != nil {
		return err
	}
	return grants.Remove(tx, g, middleware.RequestEntry(c.r, grants.ActionDelete, status))
}

// grantChangeFailed answers for a grant change that did not happen, and says
// whether it did not.
func grantChangeFailed(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNoSuchHolder):
		respond.ValidationError(w, map[string]string{"holder": "no such account"})
	case errors.Is(err, errNoSuchRole):
		respond.ValidationError(w, map[string]string{"role": "no such role"})
	case errors.Is(err, errTwoFactorRequired):
		respond.Error(w, http.StatusForbidden, "two_factor_required",
			"Changing grants needs a session signed in with two-factor authentication: sign in again with your code")
	case errors.Is(err, errNoSuchGrant):
		respond.Error(w, http.StatusNotFound, "not_found", "Grant not found")
	case errors.Is(err, grants.ErrExceedsHeld):
		respond.Error(w, http.StatusForbidden, "grant_exceeds_held", err.Error())
	case errors.Is(err, grants.ErrExists):
		respond.Error(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, grants.ErrLastSuperadmin):
		respond.Error(w, http.StatusConflict, "last_superadmin", "Refused: "+err.Error()+"; nobody could grant anything after it")
	default:
		slog.Error("grant change failed", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to change grants")
	}
	return true
}

type roleResponse struct {
	ID           uint     `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	SystemRole   bool     `json:"system_role"`
	Capabilities []string `json:"capabilities"`
}

type capabilityResponse struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
}

// ListRoles lists the roles that can be granted, each with its capabilities.
// GET /api/v1/admin/roles
func (h *GrantHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	var roles []models.Role
	if err := h.db.Order("id").Find(&roles).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch roles")
		return
	}
	out := make([]roleResponse, len(roles))
	for i, role := range roles {
		caps, err := grants.RoleCapabilities(h.db, role.ID)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch roles")
			return
		}
		if caps == nil {
			caps = []string{}
		}
		out[i] = roleResponse{ID: role.ID, Name: role.Name, Description: role.Description, SystemRole: role.SystemRole, Capabilities: caps}
	}
	respond.Data(w, http.StatusOK, out)
}

// ListCapabilities lists every capability.
// GET /api/v1/admin/capabilities
func (h *GrantHandler) ListCapabilities(w http.ResponseWriter, r *http.Request) {
	var caps []models.Capability
	if err := h.db.Order("name").Find(&caps).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch capabilities")
		return
	}
	out := make([]capabilityResponse, len(caps))
	for i, c := range caps {
		out[i] = capabilityResponse{ID: c.ID, Name: c.Name, Description: c.Description, Resource: c.Resource, Action: c.Action}
	}
	respond.Data(w, http.StatusOK, out)
}

// formatTime renders t in RFC 3339, or nil.
func formatTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.Format("2006-01-02T15:04:05Z07:00")
	return &formatted
}

// DomainScopeOf finds the domain an admin route's {id} names, for
// RequireCapability: a domain admin reaches only its own.
func DomainScopeOf(db *gorm.DB) middleware.ScopeOf {
	return func(r *http.Request) (string, bool, error) {
		id, err := pathID(r, "id")
		if err != nil {
			return "", false, nil
		}
		var names []string
		if err := db.Model(&models.Domain{}).Where("id = ?", id).Pluck("name", &names).Error; err != nil {
			return "", false, err
		}
		if len(names) == 0 {
			return "", false, nil
		}
		return grants.Domain(names[0]), true, nil
	}
}

// MailboxScopeOf finds the mailbox an admin route's {id} names, for
// RequireCapability: covered by a grant on the mailbox or on its domain.
func MailboxScopeOf(db *gorm.DB) middleware.ScopeOf {
	return func(r *http.Request) (string, bool, error) {
		id, err := pathID(r, "id")
		if err != nil {
			return "", false, nil
		}
		var addresses []string
		if err := db.Model(&models.Mailbox{}).Where("id = ?", id).Pluck("address", &addresses).Error; err != nil {
			return "", false, err
		}
		if len(addresses) == 0 {
			return "", false, nil
		}
		return grants.Mailbox(addresses[0]), true, nil
	}
}
