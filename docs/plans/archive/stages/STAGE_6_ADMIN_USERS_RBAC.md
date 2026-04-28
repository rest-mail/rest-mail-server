# Stage 6: Admin Users & RBAC - Detailed Implementation Plan

**Status:** 🚨 BLOCKED - Backend API missing
**Priority:** CRITICAL
**Estimated Effort:** 3-4 days (2 days backend, 1-2 days frontend integration)

---

## Overview

Implement complete admin user management with role-based access control (RBAC). This includes creating, editing, and deleting admin users, managing roles and capabilities, and viewing activity logs.

**Current State:**
- ✅ Frontend UI and routes complete
- ✅ Zustand stores implemented
- ❌ Backend API endpoints completely missing
- ❌ Database models exist but handlers missing

---

## Backend Implementation (Go)

### 1. Create AdminUserHandler

**File:** `internal/api/handlers/admin_user.go`

```go
package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/auth"
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
	ID                      uint     `json:"id"`
	Username                string   `json:"username"`
	Email                   *string  `json:"email"`
	Active                  bool     `json:"active"`
	PasswordChangeRequired  bool     `json:"password_change_required"`
	LastPasswordChange      *string  `json:"last_password_change"`
	CreatedAt               string   `json:"created_at"`
	UpdatedAt               string   `json:"updated_at"`
	Roles                   []roleResponse `json:"roles,omitempty"`
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

		response[i] = adminUserResponse{
			ID:                     user.ID,
			Username:               user.Username,
			Email:                  user.Email,
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

	response := adminUserResponse{
		ID:                     user.ID,
		Username:               user.Username,
		Email:                  user.Email,
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

	response := adminUserResponse{
		ID:                     user.ID,
		Username:               user.Username,
		Email:                  user.Email,
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
```

### 2. Add Routes to routes.go

**File:** `internal/api/routes.go` (around line 246, in the admin routes group)

```go
// Admin user management
adminUserH := handlers.NewAdminUserHandler(db)
r.Get("/api/v1/admin/admin-users", adminUserH.ListAdminUsers)
r.Post("/api/v1/admin/admin-users", adminUserH.CreateAdminUser)
r.Get("/api/v1/admin/admin-users/{id}", adminUserH.GetAdminUser)
r.Put("/api/v1/admin/admin-users/{id}", adminUserH.UpdateAdminUser)
r.Delete("/api/v1/admin/admin-users/{id}", adminUserH.DeleteAdminUser)

// Role and capability management
r.Get("/api/v1/admin/roles", adminUserH.ListRoles)
r.Get("/api/v1/admin/capabilities", adminUserH.ListCapabilities)
```

### 3. Verify Repository Methods Exist

**File:** `internal/db/repositories/admin_user.go`

Ensure these methods exist:
- `List()` - Get all admin users
- `GetByID(id uint)` - Get admin user by ID
- `Create(username, email, passwordHash)` - Create new admin user
- `Update(id uint, updates map[string]interface{})` - Update admin user
- `UpdatePassword(id uint, passwordHash string)` - Update password
- `Delete(id uint)` - Delete admin user
- `GetRoles(userID uint)` - Get roles for user
- `AssignRoles(userID uint, roleIDs []uint)` - Assign roles to user
- `ListRoles()` - List all roles
- `ListCapabilities()` - List all capabilities
- `GetCapabilities(userID uint)` - Get capabilities for user

---

## Frontend Integration (Already Complete)

### Files Already Implemented:
- ✅ `admin/src/lib/stores/adminUserStore.ts` - Zustand store
- ✅ `admin/src/routes/admin-users/index.tsx` - List page
- ✅ `admin/src/routes/admin-users/new.tsx` - Create page
- ✅ `admin/src/routes/admin-users/$id.tsx` - Edit page

### Testing After Backend Implementation:

1. **Test List Admin Users:**
   - Navigate to `/admin/admin-users`
   - Should display table of admin users
   - Verify filtering and search work

2. **Test Create Admin User:**
   - Click "New Admin User"
   - Fill form with username, email, password
   - Select roles
   - Submit and verify creation

3. **Test Edit Admin User:**
   - Click on admin user row
   - Edit fields (email, password, roles)
   - Submit and verify updates

4. **Test Delete Admin User:**
   - Click delete button
   - Confirm deletion
   - Verify user removed from list

---

## RBAC Integration

### 1. Add Capability Check Middleware

**File:** `internal/api/middleware/capability.go` (new file)

```go
package middleware

import (
	"net/http"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
)

// RequireCapability middleware checks if user has required capability
func RequireCapability(capability string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := r.Context().Value("claims").(*auth.Claims)
			if !ok {
				respond.Error(w, http.StatusUnauthorized, "unauthorized", "No auth claims")
				return
			}

			// Superadmin wildcard
			for _, cap := range claims.Capabilities {
				if cap == "*" || cap == capability {
					next.ServeHTTP(w, r)
					return
				}
			}

			respond.Error(w, http.StatusForbidden, "forbidden", "Insufficient permissions")
		})
	}
}

// RequireAnyCapability checks if user has any of the required capabilities
func RequireAnyCapability(capabilities ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := r.Context().Value("claims").(*auth.Claims)
			if !ok {
				respond.Error(w, http.StatusUnauthorized, "unauthorized", "No auth claims")
				return
			}

			// Check for superadmin wildcard or any matching capability
			for _, userCap := range claims.Capabilities {
				if userCap == "*" {
					next.ServeHTTP(w, r)
					return
				}
				for _, reqCap := range capabilities {
					if userCap == reqCap {
						next.ServeHTTP(w, r)
						return
					}
				}
			}

			respond.Error(w, http.StatusForbidden, "forbidden", "Insufficient permissions")
		})
	}
}
```

### 2. Apply Capability Middleware to Routes

Update routes.go to protect admin-user endpoints:

```go
// Admin user management (requires users:read for GET, users:write for POST/PUT, users:delete for DELETE)
r.With(middleware.RequireCapability("users:read")).Get("/api/v1/admin/admin-users", adminUserH.ListAdminUsers)
r.With(middleware.RequireCapability("users:write")).Post("/api/v1/admin/admin-users", adminUserH.CreateAdminUser)
r.With(middleware.RequireCapability("users:read")).Get("/api/v1/admin/admin-users/{id}", adminUserH.GetAdminUser)
r.With(middleware.RequireCapability("users:write")).Put("/api/v1/admin/admin-users/{id}", adminUserH.UpdateAdminUser)
r.With(middleware.RequireCapability("users:delete")).Delete("/api/v1/admin/admin-users/{id}", adminUserH.DeleteAdminUser)

r.With(middleware.RequireCapability("users:read")).Get("/api/v1/admin/roles", adminUserH.ListRoles)
r.With(middleware.RequireCapability("users:read")).Get("/api/v1/admin/capabilities", adminUserH.ListCapabilities)
```

### 3. Frontend Capability Hooks (TODO)

**File:** `admin/src/lib/hooks/useCapabilities.ts` (new file)

```typescript
import { useAuthStore } from '../stores/authStore'

export function useCapabilities() {
  const { user } = useAuthStore()

  const hasCapability = (capability: string): boolean => {
    if (!user || !user.capabilities) return false
    return user.capabilities.includes('*') || user.capabilities.includes(capability)
  }

  const hasAnyCapability = (capabilities: string[]): boolean => {
    return capabilities.some(cap => hasCapability(cap))
  }

  const hasAllCapabilities = (capabilities: string[]): boolean => {
    return capabilities.every(cap => hasCapability(cap))
  }

  return {
    hasCapability,
    hasAnyCapability,
    hasAllCapabilities,
    capabilities: user?.capabilities || [],
    isSuperadmin: user?.capabilities?.includes('*') || false,
  }
}
```

---

## Testing Checklist

### Backend Tests:
- [ ] List admin users returns correct data
- [ ] Get admin user by ID returns 404 for non-existent user
- [ ] Create admin user with valid data succeeds
- [ ] Create admin user with duplicate username fails
- [ ] Update admin user updates fields correctly
- [ ] Update admin user password hashes correctly
- [ ] Delete admin user removes user from database
- [ ] Assign roles updates user_roles table
- [ ] List roles returns all roles
- [ ] List capabilities returns all capabilities
- [ ] Capability middleware blocks unauthorized access
- [ ] Superadmin wildcard bypasses capability checks

### Frontend Tests:
- [ ] Admin users list loads and displays data
- [ ] Search and filter work correctly
- [ ] Create form validates input
- [ ] Create form submits successfully
- [ ] Edit form loads existing data
- [ ] Edit form updates user correctly
- [ ] Delete confirmation modal appears
- [ ] Delete removes user from list
- [ ] Role assignment multi-select works
- [ ] Capability-based UI hiding works

---

## Success Criteria

1. ✅ All 7 admin-user API endpoints functional
2. ✅ RBAC capability middleware protecting endpoints
3. ✅ Frontend can perform full CRUD on admin users
4. ✅ Role assignment working correctly
5. ✅ Capability-based UI element hiding implemented
6. ✅ Error handling with user-friendly messages
7. ✅ Activity logging for admin user changes

---

## Next Steps After Completion

1. Implement activity log viewer (Phase 6 remaining item)
2. Add role creation/editing interface
3. Add capability assignment to custom roles
4. Implement audit trail for sensitive actions
