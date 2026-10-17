package repositories

import (
	"errors"
	"fmt"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

var ErrUserNotFound = errors.New("user not found")
var ErrInvalidCredentials = errors.New("invalid credentials")

type AdminUserRepository struct {
	db *gorm.DB
}

func NewAdminUserRepository(db *gorm.DB) *AdminUserRepository {
	return &AdminUserRepository{db: db}
}

// GetByUsername retrieves a user by username.
func (r *AdminUserRepository) GetByUsername(username string) (*models.AdminUser, error) {
	var user models.AdminUser
	err := r.db.Where("username = ?", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserNotFound
	}
	return &user, err
}

// GetByID retrieves a user by ID.
func (r *AdminUserRepository) GetByID(id uint) (*models.AdminUser, error) {
	var user models.AdminUser
	err := r.db.First(&user, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserNotFound
	}
	return &user, err
}

// Create creates a new admin user with the provided details.
func (r *AdminUserRepository) Create(username string, email *string, passwordHash string) (*models.AdminUser, error) {
	user := &models.AdminUser{
		Username:     username,
		PasswordHash: passwordHash,
		Active:       true,
	}
	if email != nil {
		user.Email = *email
	}

	if err := r.db.Create(user).Error; err != nil {
		return nil, err
	}
	return user, nil
}

// Update updates an existing admin user with the provided fields.
func (r *AdminUserRepository) Update(id uint, updates map[string]interface{}) error {
	return r.db.Model(&models.AdminUser{}).Where("id = ?", id).Updates(updates).Error
}

// UpdatePassword updates the password hash for an admin user.
func (r *AdminUserRepository) UpdatePassword(id uint, passwordHash string) error {
	now := time.Now()
	return r.db.Model(&models.AdminUser{}).Where("id = ?", id).Updates(map[string]interface{}{
		"password_hash":        passwordHash,
		"last_password_change": now,
		// A new password ends every session the account had.
		"credential_epoch": gorm.Expr("credential_epoch + 1"),
	}).Error
}

// Delete deletes an admin user.
func (r *AdminUserRepository) Delete(userID uint) error {
	return r.db.Delete(&models.AdminUser{}, userID).Error
}

// List returns all admin users.
func (r *AdminUserRepository) List() ([]models.AdminUser, error) {
	var users []models.AdminUser
	err := r.db.Find(&users).Error
	return users, err
}

// ListUsers returns all admin users with pagination.
func (r *AdminUserRepository) ListUsers(limit, offset int) ([]models.AdminUser, int64, error) {
	var users []models.AdminUser
	var total int64

	if err := r.db.Model(&models.AdminUser{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := r.db.Limit(limit).Offset(offset).Find(&users).Error
	return users, total, err
}

// ListRoles returns all roles.
func (r *AdminUserRepository) ListRoles() ([]models.Role, error) {
	roleRepo := NewRoleRepository(r.db)
	return roleRepo.List()
}

// ListCapabilities returns all capabilities.
func (r *AdminUserRepository) ListCapabilities() ([]models.Capability, error) {
	capRepo := NewCapabilityRepository(r.db)
	return capRepo.List()
}

// RoleRepository handles role operations.
type RoleRepository struct {
	db *gorm.DB
}

func NewRoleRepository(db *gorm.DB) *RoleRepository {
	return &RoleRepository{db: db}
}

// GetByID retrieves a role by ID with capabilities preloaded.
func (r *RoleRepository) GetByID(id uint) (*models.Role, error) {
	var role models.Role
	err := r.db.Preload("Capabilities").First(&role, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("role not found")
	}
	return &role, err
}

// GetByName retrieves a role by name with capabilities preloaded.
func (r *RoleRepository) GetByName(name string) (*models.Role, error) {
	var role models.Role
	err := r.db.Preload("Capabilities").Where("name = ?", name).First(&role).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("role not found")
	}
	return &role, err
}

// List returns all roles.
func (r *RoleRepository) List() ([]models.Role, error) {
	var roles []models.Role
	err := r.db.Preload("Capabilities").Find(&roles).Error
	return roles, err
}

// Create creates a new role.
func (r *RoleRepository) Create(role *models.Role) error {
	return r.db.Create(role).Error
}

// Update updates an existing role.
func (r *RoleRepository) Update(role *models.Role) error {
	return r.db.Save(role).Error
}

// Delete deletes a role (only if not a system role).
func (r *RoleRepository) Delete(roleID uint) error {
	var role models.Role
	if err := r.db.First(&role, roleID).Error; err != nil {
		return err
	}
	if role.SystemRole {
		return fmt.Errorf("cannot delete system role")
	}
	return r.db.Delete(&role).Error
}

// AssignCapability assigns a capability to a role.
func (r *RoleRepository) AssignCapability(roleID, capabilityID uint) error {
	rc := models.RoleCapability{
		RoleID:       roleID,
		CapabilityID: capabilityID,
	}
	return r.db.Create(&rc).Error
}

// RevokeCapability removes a capability from a role.
func (r *RoleRepository) RevokeCapability(roleID, capabilityID uint) error {
	return r.db.Where("role_id = ? AND capability_id = ?", roleID, capabilityID).
		Delete(&models.RoleCapability{}).Error
}

// CapabilityRepository handles capability operations.
type CapabilityRepository struct {
	db *gorm.DB
}

func NewCapabilityRepository(db *gorm.DB) *CapabilityRepository {
	return &CapabilityRepository{db: db}
}

// List returns all capabilities.
func (r *CapabilityRepository) List() ([]models.Capability, error) {
	var capabilities []models.Capability
	err := r.db.Find(&capabilities).Error
	return capabilities, err
}

// GetByID retrieves a capability by ID.
func (r *CapabilityRepository) GetByID(id uint) (*models.Capability, error) {
	var cap models.Capability
	err := r.db.First(&cap, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("capability not found")
	}
	return &cap, err
}

// GetByName retrieves a capability by name.
func (r *CapabilityRepository) GetByName(name string) (*models.Capability, error) {
	var cap models.Capability
	err := r.db.Where("name = ?", name).First(&cap).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("capability not found")
	}
	return &cap, err
}

// Create creates a new capability.
func (r *CapabilityRepository) Create(cap *models.Capability) error {
	return r.db.Create(cap).Error
}
