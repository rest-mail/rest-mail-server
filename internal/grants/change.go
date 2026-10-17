package grants

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Audit actions of grant changes.
const (
	ActionCreate  = "grants.create"
	ActionDelete  = "grants.delete"
	ActionMigrate = "grants.migrate"
)

var (
	// ErrExceedsHeld: the granter does not hold every capability of the role
	// at a scope covering the grant's.
	ErrExceedsHeld = errors.New("you can only grant or remove a role whose capabilities you hold, at a scope within your own")
	// ErrExists: the holder already holds the role at that scope.
	ErrExists = errors.New("the holder already holds this role at this scope")
	// ErrLastSuperadmin: the change would leave no active admin holding every
	// capability at "*", and nobody could ever grant anything again.
	ErrLastSuperadmin = errors.New("this is the last active grant of every capability at \"*\" held by an active admin")
)

// changeLockKey serialises grant changes, so that two at once cannot each
// see the other's superadmin as the one that remains: "grantchg".
const changeLockKey int64 = 0x6772616e74636867

// Lock takes the lock every grant change holds until its transaction ends.
// Take it before reading what the change depends on.
func Lock(tx *gorm.DB) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", changeLockKey).Error
}

// Authorize checks, in tx, that the granter may grant or remove a grant of
// roleID at scope: it must hold every capability of the role at a scope
// covering scope, now. ErrExceedsHeld when it does not.
func Authorize(tx *gorm.DB, granterKind string, granterID uint, roleID uint, scope string) error {
	held, err := Load(tx, granterKind, granterID, time.Now())
	if err != nil {
		return err
	}
	caps, err := RoleCapabilities(tx, roleID)
	if err != nil {
		return err
	}
	if !held.CanGrant(caps, scope) {
		return ErrExceedsHeld
	}
	return nil
}

// Add inserts g and the audit entry recording it, in tx; entry says who is
// acting and through which request. ErrExists if the holder already holds
// that role at that scope. The caller holds Lock and has checked Authorize.
func Add(tx *gorm.DB, g *models.Grant, entry models.AuditEntry) error {
	if g.GrantedAt.IsZero() {
		g.GrantedAt = time.Now()
	}
	// The grant alone: never its role, which a caller may have loaded into it.
	res := tx.Omit(clause.Associations).Clauses(clause.OnConflict{DoNothing: true}).Create(g)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrExists
	}
	entry.Action = ActionCreate
	return appendChange(tx, g, entry)
}

// Remove deletes g and writes the audit entry recording it, in tx, unless it
// is the last active grant of every capability at "*" held by an active admin
// (ErrLastSuperadmin). The caller holds Lock and has checked Authorize.
func Remove(tx *gorm.DB, g *models.Grant, entry models.AuditEntry) error {
	if err := tx.Delete(&models.Grant{}, g.ID).Error; err != nil {
		return err
	}
	var remaining int64
	err := tx.Table("grants AS g").
		Joins("JOIN admin_roles_capabilities rc ON rc.role_id = g.role_id").
		Joins("JOIN admin_capabilities c ON c.id = rc.capability_id").
		Joins("JOIN admin_users u ON u.id = g.holder_id").
		Where("g.holder_kind = ? AND g.scope = ? AND g.mode = ? AND c.name = ? AND u.active", models.GrantHolderAdmin, All, models.GrantActive, Wildcard).
		Where("g.expires_at IS NULL OR g.expires_at > ?", time.Now()).
		Count(&remaining).Error
	if err != nil {
		return err
	}
	if remaining == 0 && wasSuperadmin(tx, g) {
		return ErrLastSuperadmin
	}
	entry.Action = ActionDelete
	return appendChange(tx, g, entry)
}

// wasSuperadmin reports whether g, just deleted, was an active grant of every
// capability at "*" held by an active admin.
func wasSuperadmin(tx *gorm.DB, g *models.Grant) bool {
	if g.Scope != All || g.Mode != models.GrantActive || g.HolderKind != models.GrantHolderAdmin {
		return false
	}
	if g.ExpiresAt != nil && !g.ExpiresAt.After(time.Now()) {
		return false
	}
	var active []bool
	if err := tx.Model(&models.AdminUser{}).Where("id = ?", g.HolderID).Pluck("active", &active).Error; err != nil {
		return true // cannot tell: refuse, keep the grant
	}
	if len(active) == 0 || !active[0] {
		return false
	}
	caps, err := RoleCapabilities(tx, g.RoleID)
	if err != nil {
		return true // cannot tell: refuse, keep the grant
	}
	for _, c := range caps {
		if c == Wildcard {
			return true
		}
	}
	return false
}

// EnsureSystem gives the holder the role at scope in mode, as the system (the
// bootstrap admin, the seed), recording it in the audit log in the same
// transaction. Nothing changes, and nothing is recorded, if the holder already
// holds the role at that scope.
func EnsureSystem(tx *gorm.DB, holderKind string, holderID, roleID uint, scope, mode string) (bool, error) {
	g := &models.Grant{
		HolderKind:    holderKind,
		HolderID:      holderID,
		RoleID:        roleID,
		Scope:         scope,
		Mode:          mode,
		GrantedByKind: models.AuditActorSystem,
	}
	err := Add(tx, g, models.AuditEntry{ActorKind: models.AuditActorSystem, ActorName: "system"})
	if errors.Is(err, ErrExists) {
		return false, nil
	}
	return err == nil, err
}

// appendChange writes the audit entry for a change to g.
func appendChange(tx *gorm.DB, g *models.Grant, entry models.AuditEntry) error {
	entry.Outcome = models.AuditOutcomeSuccess
	if entry.Target == nil {
		entry.Target = models.AuditTarget{}
	}
	for k, v := range Describe(tx, g) {
		entry.Target[k] = v
	}
	if err := audit.Append(tx, &entry); err != nil {
		return fmt.Errorf("record the grant change in the audit log: %w", err)
	}
	return nil
}

// Describe is what the audit log records of g.
func Describe(tx *gorm.DB, g *models.Grant) models.AuditTarget {
	t := models.AuditTarget{
		"grant_id":    strconv.FormatUint(uint64(g.ID), 10),
		"holder_kind": g.HolderKind,
		"holder_id":   strconv.FormatUint(uint64(g.HolderID), 10),
		"role_id":     strconv.FormatUint(uint64(g.RoleID), 10),
		"scope":       g.Scope,
		"mode":        g.Mode,
	}
	if g.Role.Name != "" {
		t["role"] = g.Role.Name
	} else {
		var names []string
		if tx.Model(&models.Role{}).Where("id = ?", g.RoleID).Pluck("name", &names).Error == nil && len(names) == 1 {
			t["role"] = names[0]
		}
	}
	if g.ExpiresAt != nil {
		t["expires_at"] = g.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return t
}
