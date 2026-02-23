package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

type AdminUserHandler struct {
	db *gorm.DB
}

func NewAdminUserHandler(db *gorm.DB) *AdminUserHandler {
	return &AdminUserHandler{db: db}
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
	Username string   `json:"username"`
	Email    *string  `json:"email"`
	Password string   `json:"password"`
	RoleIDs  []uint   `json:"role_ids"`
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

	users, err := repo.List()
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to fetch admin users")
		return
	}

	response := make([]adminUserResponse, len(users))
	for i, user := range users {
		// Load roles for each user
		roles, _ := repo.GetRoles(user.ID)
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

	respond.Data(w, http.StatusOK, response)
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

	roles, _ := repo.GetRoles(user.ID)
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
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to hash password")
		return
	}

	repo := repositories.NewAdminUserRepository(h.db)

	// Create user
	user, err := repo.Create(req.Username, req.Email, passwordHash)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create admin user")
		return
	}

	// Assign roles
	if len(req.RoleIDs) > 0 {
		if err := repo.AssignRoles(user.ID, req.RoleIDs); err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to assign roles")
			return
		}
	}

	// Return created user
	roles, _ := repo.GetRoles(user.ID)
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

	// Update password if provided
	if req.Password != nil && *req.Password != "" {
		passwordHash, err := auth.HashPassword(*req.Password)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to hash password")
			return
		}
		if err := repo.UpdatePassword(uint(id), passwordHash); err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update password")
			return
		}
	}

	// Update other fields
	updates := make(map[string]interface{})
	if req.Email != nil {
		updates["email"] = req.Email
	}
	if req.Active != nil {
		updates["active"] = *req.Active
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

	// Update roles if provided
	if req.RoleIDs != nil {
		if err := repo.AssignRoles(uint(id), req.RoleIDs); err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update roles")
			return
		}
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

	repo := repositories.NewAdminUserRepository(h.db)
	if err := repo.Delete(uint(id)); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete admin user")
		return
	}

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

// Helper function
func formatTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.Format("2006-01-02T15:04:05Z07:00")
	return &formatted
}
