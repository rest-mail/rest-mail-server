package models

import "time"

// GrantHolderAdmin is the one kind of holder grants have for now: an admin
// user. HolderKind is open-ended text, not an enum, because any object may
// later hold grants (#341): a mailbox, a credential, an agent, a pipeline.
const GrantHolderAdmin = "admin"

// A grant's mode. An active grant is in force; an eligible one does nothing
// until its holder switches it on for a while (GrantActivation, #346).
const (
	GrantActive   = "active"
	GrantEligible = "eligible"
)

// Grant gives its holder a role at a scope: "*" (the whole installation),
// "domain:<name>" or "mailbox:<address>". A grant covers everything beneath
// its scope (see internal/grants). It replaces the role assignments of
// admin_users_roles, which AutoMigrate copied into grants once, each as an
// active grant at "*" (db.MigrateGrants).
//
// One holder holds a role at a scope at most once, active or eligible.
type Grant struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	HolderKind string `gorm:"type:text;not null;uniqueIndex:idx_grants_holder_role_scope,priority:1" json:"holder_kind"`
	HolderID   uint   `gorm:"not null;uniqueIndex:idx_grants_holder_role_scope,priority:2" json:"holder_id"`
	RoleID     uint   `gorm:"not null;uniqueIndex:idx_grants_holder_role_scope,priority:3;index" json:"role_id"`
	Scope      string `gorm:"type:text;not null;uniqueIndex:idx_grants_holder_role_scope,priority:4" json:"scope"`
	Mode       string `gorm:"type:text;not null" json:"mode"`
	// Who granted it: an admin, or the system (the migration, the bootstrap
	// admin, the seed), in the audit log's actor kinds. GrantedByID is nil for
	// the system.
	GrantedByKind string     `gorm:"type:text;not null" json:"granted_by_kind"`
	GrantedByID   *uint      `json:"granted_by_id"`
	GrantedAt     time.Time  `gorm:"not null" json:"granted_at"`
	ExpiresAt     *time.Time `json:"expires_at"`

	Role Role `gorm:"foreignKey:RoleID" json:"-"`
}

func (Grant) TableName() string { return "grants" }
