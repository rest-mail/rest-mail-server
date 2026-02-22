package models

import "time"

// AdminUser represents an administrative user with role-based permissions.
// Separate from mailbox users - these are for system/service administration.
type AdminUser struct {
	ID                     uint      `gorm:"primaryKey" json:"id"`
	Username               string    `gorm:"size:255;uniqueIndex;not null" json:"username"`
	Email                  string    `gorm:"size:255" json:"email"` // optional, not unique
	PasswordHash           string    `gorm:"size:255;not null" json:"-"`
	PasswordChangeRequired bool      `gorm:"default:false" json:"password_change_required"`
	LastPasswordChange     *time.Time `json:"last_password_change,omitempty"`
	Active                 bool      `gorm:"default:true" json:"active"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`

	// Relationships
	Roles []Role `gorm:"many2many:admin_users_roles;joinForeignKey:UserID;joinReferences:RoleID" json:"roles,omitempty"`
}

func (AdminUser) TableName() string { return "admin_users" }

// Role represents a group of capabilities that can be assigned to users.
type Role struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:100;uniqueIndex;not null" json:"name"`
	Description string    `gorm:"type:text" json:"description"`
	SystemRole  bool      `gorm:"default:false" json:"system_role"` // protected from deletion
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Relationships
	Capabilities []Capability `gorm:"many2many:roles_capabilities;" json:"capabilities,omitempty"`
}

func (Role) TableName() string { return "admin_roles" }

// Capability represents an atomic permission (e.g., "domains:read", "mailboxes:write").
type Capability struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:100;uniqueIndex;not null" json:"name"`
	Description string    `gorm:"type:text" json:"description"`
	Resource    string    `gorm:"size:50" json:"resource"` // e.g., "domains", "mailboxes"
	Action      string    `gorm:"size:50" json:"action"`   // e.g., "read", "write", "delete"
	CreatedAt   time.Time `json:"created_at"`
}

func (Capability) TableName() string { return "admin_capabilities" }

// UserRole is the junction table tracking user-role assignments with audit info.
type UserRole struct {
	UserID    uint       `gorm:"primaryKey" json:"user_id"`
	RoleID    uint       `gorm:"primaryKey" json:"role_id"`
	GrantedAt time.Time  `gorm:"not null;default:CURRENT_TIMESTAMP" json:"granted_at"`
	GrantedBy *uint      `json:"granted_by,omitempty"` // admin user who granted this role

	User      AdminUser  `gorm:"foreignKey:UserID" json:"-"`
	Role      Role       `gorm:"foreignKey:RoleID" json:"-"`
}

func (UserRole) TableName() string { return "admin_users_roles" }

// RoleCapability is the junction table mapping roles to capabilities.
type RoleCapability struct {
	RoleID       uint       `gorm:"primaryKey" json:"role_id"`
	CapabilityID uint       `gorm:"primaryKey" json:"capability_id"`
	GrantedAt    time.Time  `gorm:"not null;default:CURRENT_TIMESTAMP" json:"granted_at"`

	Role         Role       `gorm:"foreignKey:RoleID" json:"-"`
	Capability   Capability `gorm:"foreignKey:CapabilityID" json:"-"`
}

func (RoleCapability) TableName() string { return "admin_roles_capabilities" }
