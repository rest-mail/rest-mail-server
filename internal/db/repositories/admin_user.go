package repositories

import (
	"errors"
	"fmt"

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

// GetByUsername retrieves a user by username with their roles preloaded.
func (r *AdminUserRepository) GetByUsername(username string) (*models.AdminUser, error) {
	var user models.AdminUser
	err := r.db.Preload("Roles").Where("username = ?", username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserNotFound
	}
	return &user, err
}

// GetByID retrieves a user by ID with their roles preloaded.
func (r *AdminUserRepository) GetByID(id uint) (*models.AdminUser, error) {
	var user models.AdminUser
	err := r.db.Preload("Roles").First(&user, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserNotFound
	}
	return &user, err
}

// GetCapabilities returns all capabilities for a user across all their roles.
func (r *AdminUserRepository) GetCapabilities(userID uint) ([]models.Capability, error) {
	var capabilities []models.Capability

	// Get all role IDs for this user
	var userRoles []models.UserRole
	if err := r.db.Where("user_id = ?", userID).Find(&userRoles).Error; err != nil {
		return nil, err
	}

	if len(userRoles) == 0 {
		return capabilities, nil
	}

	roleIDs := make([]uint, len(userRoles))
	for i, ur := range userRoles {
		roleIDs[i] = ur.RoleID
	}

	// Get all capabilities for these roles
	err := r.db.Joins("JOIN admin_roles_capabilities ON admin_roles_capabilities.capability_id = admin_capabilities.id").
		Where("admin_roles_capabilities.role_id IN ?", roleIDs).
		Distinct().
		Find(&capabilities).Error

	return capabilities, err
}

// HasCapability checks if a user has a specific capability.
// Handles wildcard "*" permission.
func (r *AdminUserRepository) HasCapability(userID uint, capabilityName string) (bool, error) {
	capabilities, err := r.GetCapabilities(userID)
	if err != nil {
		return false, err
	}

	for _, cap := range capabilities {
		if cap.Name == "*" || cap.Name == capabilityName {
			return true, nil
		}
	}

	return false, nil
}

// Create creates a new admin user.
func (r *AdminUserRepository) Create(user *models.AdminUser) error {
	return r.db.Create(user).Error
}

// Update updates an existing admin user.
func (r *AdminUserRepository) Update(user *models.AdminUser) error {
	return r.db.Save(user).Error
}

// Delete deletes an admin user.
func (r *AdminUserRepository) Delete(userID uint) error {
	return r.db.Delete(&models.AdminUser{}, userID).Error
}

// ListUsers returns all admin users with pagination.
func (r *AdminUserRepository) ListUsers(limit, offset int) ([]models.AdminUser, int64, error) {
	var users []models.AdminUser
	var total int64

	if err := r.db.Model(&models.AdminUser{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := r.db.Preload("Roles").Limit(limit).Offset(offset).Find(&users).Error
	return users, total, err
}

// AssignRole assigns a role to a user.
func (r *AdminUserRepository) AssignRole(userID, roleID uint, grantedBy *uint) error {
	userRole := models.UserRole{
		UserID:    userID,
		RoleID:    roleID,
		GrantedBy: grantedBy,
	}
	return r.db.Create(&userRole).Error
}

// RevokeRole removes a role from a user.
func (r *AdminUserRepository) RevokeRole(userID, roleID uint) error {
	return r.db.Where("user_id = ? AND role_id = ?", userID, roleID).
		Delete(&models.UserRole{}).Error
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
