package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// AdminUserHandler manages admin users. An admin's roles are its active
// grants at "*": role_ids reads and sets those, through the same rules as the
// granting API (GrantHandler), and leaves grants at narrower scopes, and
// eligible ones, alone.
type AdminUserHandler struct {
	db *gorm.DB
	// cache is the one RequireCapability reads; every grant change empties it.
	cache *grants.Cache
}

func NewAdminUserHandler(db *gorm.DB, cache *grants.Cache) *AdminUserHandler {
	return &AdminUserHandler{db: db, cache: cache}
}

// invalidate empties the grants cache after a grant change.
func (h *AdminUserHandler) invalidate() {
	if h.cache != nil {
		h.cache.Invalidate()
	}
}

// revokeSessions best-effort invalidates every live session for an admin user
// after a security-relevant change (password change, disable, role change,
// delete), so an existing refresh token can no longer be exchanged. Failures are
// logged, not fatal: the primary mutation has already succeeded.
func (h *AdminUserHandler) revokeSessions(adminUserID uint) {
	if h.db == nil {
		return
	}
	if err := repositories.NewRefreshTokenRepository(h.db).RevokeAllForSubject("admin", adminUserID); err != nil {
		slog.Warn("failed to revoke admin sessions", "admin_user_id", adminUserID, "error", err)
	}
}

// Request/Response types
type adminUserResponse struct {
	ID                     uint           `json:"id"`
	Username               string         `json:"username"`
	Email                  *string        `json:"email"`
	Active                 bool           `json:"active"`
	PasswordChangeRequired bool           `json:"password_change_required"`
	LastPasswordChange     *string        `json:"last_password_change"`
	CreatedAt              string         `json:"created_at"`
	UpdatedAt              string         `json:"updated_at"`
	Roles                  []roleResponse `json:"roles,omitempty"`
}

type roleResponse struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SystemRole  bool   `json:"system_role"`
}

type capabilityResponse struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
}

type createAdminUserRequest struct {
	Username string  `json:"username"`
	Email    *string `json:"email"`
	Password string  `json:"password"`
	RoleIDs  []uint  `json:"role_ids"`
}

type updateAdminUserRequest struct {
	Email                  *string `json:"email"`
	Password               *string `json:"password"`
	Active                 *bool   `json:"active"`
	PasswordChangeRequired *bool   `json:"password_change_required"`
	RoleIDs                []uint  `json:"role_ids"`
}

// ListAdminUsers returns all admin users
func (h *AdminUserHandler) ListAdminUsers(w http.ResponseWriter, r *http.Request) {
	repo := repositories.NewAdminUserRepository(h.db)

	limit, offset := parsePagination(r, defaultListLimit, maxListLimit)
	users, total, err := repo.ListUsers(limit, offset)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch admin users")
		return
	}

	response := make([]adminUserResponse, len(users))
	for i, user := range users {
		// Load roles for each user
		roles, _ := rolesAtAll(h.db, user.ID)
		roleResponses := make([]roleResponse, len(roles))
		for j, role := range roles {
			roleResponses[j] = roleResponse{
				ID:          role.ID,
				Name:        role.Name,
				Description: role.Description,
				SystemRole:  role.SystemRole,
			}
		}

		var email *string
		if user.Email != "" {
			email = &user.Email
		}

		response[i] = adminUserResponse{
			ID:                     user.ID,
			Username:               user.Username,
			Email:                  email,
			Active:                 user.Active,
			PasswordChangeRequired: user.PasswordChangeRequired,
			LastPasswordChange:     formatTime(user.LastPasswordChange),
			CreatedAt:              user.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt:              user.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
			Roles:                  roleResponses,
		}
	}

	respond.List(w, response, &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total})
}

// GetAdminUser returns a single admin user by ID
func (h *AdminUserHandler) GetAdminUser(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_id", "Invalid admin user ID")
		return
	}

	repo := repositories.NewAdminUserRepository(h.db)
	user, err := repo.GetByID(uint(id))
	if err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Admin user not found")
		return
	}

	roles, _ := rolesAtAll(h.db, user.ID)
	roleResponses := make([]roleResponse, len(roles))
	for i, role := range roles {
		roleResponses[i] = roleResponse{
			ID:          role.ID,
			Name:        role.Name,
			Description: role.Description,
			SystemRole:  role.SystemRole,
		}
	}

	var email *string
	if user.Email != "" {
		email = &user.Email
	}

	response := adminUserResponse{
		ID:                     user.ID,
		Username:               user.Username,
		Email:                  email,
		Active:                 user.Active,
		PasswordChangeRequired: user.PasswordChangeRequired,
		LastPasswordChange:     formatTime(user.LastPasswordChange),
		CreatedAt:              user.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:              user.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Roles:                  roleResponses,
	}

	respond.Data(w, http.StatusOK, response)
}

// CreateAdminUser creates a new admin user
func (h *AdminUserHandler) CreateAdminUser(w http.ResponseWriter, r *http.Request) {
	var req createAdminUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Validate
	if req.Username == "" || req.Password == "" {
		respond.ValidationError(w, map[string]string{
			"username": "required",
			"password": "required",
		})
		return
	}

	// Hash password
	passwordHash, err := hashPassword(req.Password)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to hash password")
		return
	}

	// The account and its roles are made together, or not at all: roles
	// refused (no 2FA session, more than the caller holds) create no account.
	var user *models.AdminUser
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if len(req.RoleIDs) > 0 {
			if err := grants.Lock(tx); err != nil {
				return err
			}
		}
		var err error
		user, err = repositories.NewAdminUserRepository(tx).Create(req.Username, req.Email, passwordHash)
		if err != nil {
			return errCreateAdminUser
		}
		if len(req.RoleIDs) > 0 {
			change := grantChange{r: r, claims: middleware.GetClaims(r)}
			if _, err := change.setRolesAtAll(tx, user.ID, req.RoleIDs, http.StatusCreated); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errCreateAdminUser) {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create admin user")
		return
	}
	if grantChangeFailed(w, err) {
		return
	}
	if len(req.RoleIDs) > 0 {
		h.invalidate()
	}

	// Return created user
	roles, _ := rolesAtAll(h.db, user.ID)
	roleResponses := make([]roleResponse, len(roles))
	for i, role := range roles {
		roleResponses[i] = roleResponse{
			ID:          role.ID,
			Name:        role.Name,
			Description: role.Description,
			SystemRole:  role.SystemRole,
		}
	}

	var email *string
	if user.Email != "" {
		email = &user.Email
	}

	response := adminUserResponse{
		ID:                     user.ID,
		Username:               user.Username,
		Email:                  email,
		Active:                 user.Active,
		PasswordChangeRequired: user.PasswordChangeRequired,
		LastPasswordChange:     formatTime(user.LastPasswordChange),
		CreatedAt:              user.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:              user.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Roles:                  roleResponses,
	}

	respond.Data(w, http.StatusCreated, response)
}

// UpdateAdminUser updates an existing admin user
func (h *AdminUserHandler) UpdateAdminUser(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_id", "Invalid admin user ID")
		return
	}

	var req updateAdminUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	repo := repositories.NewAdminUserRepository(h.db)

	// revokeSessions tracks whether this update is security-relevant (password
	// change, disable, or role/capability change) and therefore must invalidate
	// the user's live sessions once all changes have been applied.
	revokeSessions := false

	// Roles first: a role change that is refused changes nothing else. Sending
	// the roles the account already holds (as the admin UI does on every save)
	// is not a change, and needs no 2FA session.
	if req.RoleIDs != nil {
		changed := false
		err := h.db.Transaction(func(tx *gorm.DB) error {
			if err := grants.Lock(tx); err != nil {
				return err
			}
			var err error
			change := grantChange{r: r, claims: middleware.GetClaims(r)}
			changed, err = change.setRolesAtAll(tx, uint(id), req.RoleIDs, http.StatusOK)
			return err
		})
		if grantChangeFailed(w, err) {
			return
		}
		if changed {
			h.invalidate()
			revokeSessions = true
		}
	}

	// Update password if provided
	if req.Password != nil && *req.Password != "" {
		passwordHash, err := hashPassword(*req.Password)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to hash password")
			return
		}
		if err := repo.UpdatePassword(uint(id), passwordHash); err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update password")
			return
		}
		revokeSessions = true
	}

	// Update other fields
	updates := make(map[string]interface{})
	if req.Email != nil {
		updates["email"] = req.Email
	}
	if req.Active != nil {
		updates["active"] = *req.Active
		if !*req.Active {
			revokeSessions = true
		}
	}
	if req.PasswordChangeRequired != nil {
		updates["password_change_required"] = *req.PasswordChangeRequired
	}

	if len(updates) > 0 {
		if err := repo.Update(uint(id), updates); err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update admin user")
			return
		}
	}

	if revokeSessions {
		h.revokeSessions(uint(id))
	}

	respond.Data(w, http.StatusOK, map[string]string{"message": "Admin user updated successfully"})
}

// DeleteAdminUser deletes an admin user
func (h *AdminUserHandler) DeleteAdminUser(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_id", "Invalid admin user ID")
		return
	}

	// The account's grants go with it, each one removed as the granting API
	// would remove it (a 2FA session, no more than the caller holds, never the
	// last superadmin) and recorded, in the same transaction as the account.
	removed := false
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		var held []models.Grant
		if err := tx.Preload("Role").Where("holder_kind = ? AND holder_id = ?", models.GrantHolderAdmin, id).Order("id").Find(&held).Error; err != nil {
			return err
		}
		claims := middleware.GetClaims(r)
		if len(held) > 0 && (claims == nil || !claims.HasAMR(auth.AMROTP)) {
			return errTwoFactorRequired
		}
		change := grantChange{r: r, claims: claims}
		for i := range held {
			if err := change.remove(tx, &held[i], http.StatusNoContent); err != nil {
				return err
			}
		}
		removed = len(held) > 0
		// Its role assignments from before grants, kept for one release, go too:
		// they would otherwise keep the account from being deleted.
		if err := tx.Where("user_id = ?", id).Delete(&models.UserRole{}).Error; err != nil {
			return err
		}
		if err := repositories.NewAdminUserRepository(tx).Delete(uint(id)); err != nil {
			return errDeleteAdminUser
		}
		return nil
	})
	if errors.Is(err, errDeleteAdminUser) {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete admin user")
		return
	}
	if grantChangeFailed(w, err) {
		return
	}
	if removed {
		h.invalidate()
	}

	// A deleted admin must not keep a usable session.
	h.revokeSessions(uint(id))

	w.WriteHeader(http.StatusNoContent)
}

// ListRoles returns all roles
func (h *AdminUserHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	repo := repositories.NewAdminUserRepository(h.db)

	roles, err := repo.ListRoles()
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch roles")
		return
	}

	response := make([]roleResponse, len(roles))
	for i, role := range roles {
		response[i] = roleResponse{
			ID:          role.ID,
			Name:        role.Name,
			Description: role.Description,
			SystemRole:  role.SystemRole,
		}
	}

	respond.Data(w, http.StatusOK, response)
}

// ListCapabilities returns all capabilities
func (h *AdminUserHandler) ListCapabilities(w http.ResponseWriter, r *http.Request) {
	repo := repositories.NewAdminUserRepository(h.db)

	capabilities, err := repo.ListCapabilities()
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch capabilities")
		return
	}

	response := make([]capabilityResponse, len(capabilities))
	for i, cap := range capabilities {
		response[i] = capabilityResponse{
			ID:          cap.ID,
			Name:        cap.Name,
			Description: cap.Description,
			Resource:    cap.Resource,
			Action:      cap.Action,
		}
	}

	respond.Data(w, http.StatusOK, response)
}

var (
	errCreateAdminUser = errors.New("create admin user")
	errDeleteAdminUser = errors.New("delete admin user")
)

// rolesAtAll lists the roles an admin holds actively at "*", by id: what
// role_ids reads and sets.
func rolesAtAll(db *gorm.DB, adminID uint) ([]models.Role, error) {
	var roles []models.Role
	err := db.Model(&models.Role{}).
		Joins("JOIN grants g ON g.role_id = admin_roles.id").
		Where("g.holder_kind = ? AND g.holder_id = ? AND g.scope = ? AND g.mode = ?", models.GrantHolderAdmin, adminID, grants.All, models.GrantActive).
		Where("g.expires_at IS NULL OR g.expires_at > ?", time.Now()).
		Order("admin_roles.id").
		Find(&roles).Error
	return roles, err
}

// setRolesAtAll makes the roles holderID holds actively at "*", without an
// expiry, exactly roleIDs: each grant it adds or removes is checked against
// what the caller holds and recorded, in tx, which holds grants.Lock. It says
// whether anything changed; a change needs a 2FA session.
func (c grantChange) setRolesAtAll(tx *gorm.DB, holderID uint, roleIDs []uint, status int) (bool, error) {
	var holder models.AdminUser
	if err := tx.Select("id").First(&holder, holderID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, errNoSuchHolder
		}
		return false, err
	}
	var current []models.Grant
	if err := tx.Preload("Role").
		Where("holder_kind = ? AND holder_id = ? AND scope = ? AND mode = ?", models.GrantHolderAdmin, holderID, grants.All, models.GrantActive).
		Order("id").Find(&current).Error; err != nil {
		return false, err
	}
	want := map[uint]bool{}
	for _, id := range roleIDs {
		want[id] = true
	}
	now := time.Now()
	var remove []models.Grant
	have := map[uint]bool{}
	for _, g := range current {
		expired := g.ExpiresAt != nil && !g.ExpiresAt.After(now)
		if !want[g.RoleID] || expired {
			remove = append(remove, g)
		} else {
			have[g.RoleID] = true
		}
	}
	var add []uint
	for _, id := range roleIDs {
		if !have[id] {
			add = append(add, id)
			have[id] = true
		}
	}
	if len(add) == 0 && len(remove) == 0 {
		return false, nil
	}
	if c.claims == nil || !c.claims.HasAMR(auth.AMROTP) {
		return false, errTwoFactorRequired
	}
	for i := range remove {
		if err := c.remove(tx, &remove[i], status); err != nil {
			return false, err
		}
	}
	for _, roleID := range add {
		g := &models.Grant{
			HolderKind: models.GrantHolderAdmin,
			HolderID:   holderID,
			Scope:      grants.All,
			Mode:       models.GrantActive,
		}
		if err := tx.First(&g.Role, roleID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, errNoSuchRoleID
			}
			return false, err
		}
		g.RoleID = g.Role.ID
		if err := c.add(tx, g, status); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Helper function
func formatTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.Format("2006-01-02T15:04:05Z07:00")
	return &formatted
}
