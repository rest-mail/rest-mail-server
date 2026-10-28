package models

import "time"

// Role is a named bundle of capabilities, granted to an account at a scope
// (Grant).
type Role struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:100;uniqueIndex;not null" json:"name"`
	Description string    `gorm:"type:text" json:"description"`
	SystemRole  bool      `gorm:"default:false" json:"system_role"` // protected from deletion
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (Role) TableName() string { return "roles" }

// Capability is one thing that can be done: "domains:read", "mail:send", or
// "*", every one of them.
type Capability struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:100;uniqueIndex;not null" json:"name"`
	Description string    `gorm:"type:text" json:"description"`
	Resource    string    `gorm:"size:50" json:"resource"` // e.g., "domains", "mail"
	Action      string    `gorm:"size:50" json:"action"`   // e.g., "read", "write", "delete"
	CreatedAt   time.Time `json:"created_at"`
}

func (Capability) TableName() string { return "capabilities" }

// RoleCapability puts a capability in a role.
type RoleCapability struct {
	RoleID       uint      `gorm:"primaryKey" json:"role_id"`
	CapabilityID uint      `gorm:"primaryKey" json:"capability_id"`
	GrantedAt    time.Time `gorm:"not null;default:CURRENT_TIMESTAMP" json:"granted_at"`

	Role       Role       `gorm:"foreignKey:RoleID" json:"-"`
	Capability Capability `gorm:"foreignKey:CapabilityID" json:"-"`
}

func (RoleCapability) TableName() string { return "role_capabilities" }

// The mail capabilities an account holds on a mailbox. The owner role holds
// all of these. Receive is an explicit, revocable own-mailbox grant; the
// others are implicit (grants.Load).
const (
	CapMailRead         = "mail:read"
	CapMailWrite        = "mail:write"
	CapMailSend         = "mail:send"
	CapMailReceive      = "mail:receive"
	CapMailSendAs       = "mail:send_as"
	CapMailSendOnBehalf = "mail:send_on_behalf"
	CapMailboxManage    = "mailbox:manage"
)

// OwnerCapabilities are the owner role's: everything on one mailbox.
var OwnerCapabilities = []string{CapMailRead, CapMailWrite, CapMailSend, CapMailReceive, CapMailSendAs, CapMailSendOnBehalf, CapMailboxManage}

// RoleOwner is the role an account holds on a mailbox it created or linked.
const RoleOwner = "owner"

// RoleReceiver holds only the independently revocable receive capability.
const RoleReceiver = "receiver"
