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
	ActionCreate = "grants.create"
	ActionDelete = "grants.delete"
)

var (
	// ErrExceedsHeld: the granter does not hold every capability of the role
	// at a scope covering the grant's.
	ErrExceedsHeld = errors.New("you can only grant or remove a role whose capabilities you hold, at a scope within your own")
	// ErrExists: the holder already holds the role at that scope.
	ErrExists = errors.New("the holder already holds this role at this scope")
	// ErrLastSuperadmin: the change would leave no active admin holding every
	// capability at "*", and nobody could ever grant anything again. An
	// eligible grant counts when its holder has an authenticator to switch it
	// on with.
	ErrLastSuperadmin = errors.New("this is the last grant of every capability at \"*\" held by an active account that can use it")
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
func Authorize(tx *gorm.DB, granterID uint, roleID uint, scope string) error {
	held, err := Load(tx, granterID, time.Now())
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
	// The server ceiling changes only through SetServerCeiling, by a
	// superadmin. Account-grant operations must not become an unauthorised
	// way to change installation policy.
	if g.HolderKind == models.GrantHolderServer {
		return ErrExceedsHeld
	}
	// The ceiling role belongs only to the server object. Assigning it to an
	// account would turn a later increase of the ceiling into an actor grant.
	var serverRole int64
	if err := tx.Model(&models.Role{}).Where("id = ? AND name = ?", g.RoleID, models.RoleServer).Count(&serverRole).Error; err != nil {
		return err
	}
	if serverRole != 0 {
		return ErrExceedsHeld
	}
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
// is the last grant of every capability at "*" held by an active account that
// can use it (ErrLastSuperadmin, usableSuperadmin). An activation of g still
// running ends with it, and is recorded as switched off by the same actor. The
// caller holds Lock and has checked Authorize.
func Remove(tx *gorm.DB, g *models.Grant, entry models.AuditEntry) error {
	if g.HolderKind == models.GrantHolderServer {
		return ErrExceedsHeld
	}
	now := time.Now()
	live, err := liveActivation(tx, g.ID, now)
	if err != nil {
		return err
	}
	// The grant's activations go with it; the audit log keeps their history.
	if err := tx.Where("grant_id = ?", g.ID).Delete(&models.GrantActivation{}).Error; err != nil {
		return err
	}
	if err := tx.Delete(&models.Grant{}, g.ID).Error; err != nil {
		return err
	}
	var remaining int64
	err = tx.Table("grants AS g").
		Joins("JOIN role_capabilities rc ON rc.role_id = g.role_id").
		Joins("JOIN capabilities c ON c.id = rc.capability_id").
		Joins("JOIN mailboxes u ON u.id = g.holder_id").
		Where("g.holder_kind = ? AND g.scope = ? AND c.name = ? AND u.active", models.GrantHolderAccount, All, Wildcard).
		Where("g.mode = ? OR (g.mode = ? AND "+canSwitchOn+")", models.GrantActive, models.GrantEligible, models.CredentialKindTOTP).
		Where("g.expires_at IS NULL OR g.expires_at > ?", now).
		Count(&remaining).Error
	if err != nil {
		return err
	}
	if remaining == 0 && wasSuperadmin(tx, g) {
		return ErrLastSuperadmin
	}
	if live != nil {
		live.EndedAt = &now
		ended := entry
		ended.Action = ActionDeactivate
		if err := appendActivation(tx, g, live, ended); err != nil {
			return err
		}
	}
	entry.Action = ActionDelete
	return appendChange(tx, g, entry)
}

// canSwitchOn is the condition that the account holding grant g has a
// confirmed authenticator: switching an eligible grant on needs a code from one.
const canSwitchOn = "EXISTS (SELECT 1 FROM credentials tf WHERE tf.kind = ? AND tf.account_id = g.holder_id AND tf.confirmed_at IS NOT NULL AND tf.revoked_at IS NULL)"

// wasSuperadmin reports whether g, just deleted, was a grant of every
// capability at "*" held by an active account that could use it: active, or
// eligible with an authenticator to switch it on with.
func wasSuperadmin(tx *gorm.DB, g *models.Grant) bool {
	if g.Scope != All || g.HolderKind != models.GrantHolderAccount {
		return false
	}
	if g.ExpiresAt != nil && !g.ExpiresAt.After(time.Now()) {
		return false
	}
	switch g.Mode {
	case models.GrantActive:
	case models.GrantEligible:
		var n int64
		err := tx.Model(&models.Credential{}).
			Where("kind = ? AND account_id = ? AND confirmed_at IS NOT NULL AND revoked_at IS NULL", models.CredentialKindTOTP, g.HolderID).
			Count(&n).Error
		if err != nil {
			return true // cannot tell: refuse, keep the grant
		}
		if n == 0 {
			return false
		}
	default:
		return false
	}
	var active []bool
	if err := tx.Model(&models.Mailbox{}).Where("id = ?", g.HolderID).Pluck("active", &active).Error; err != nil {
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
// bootstrap account, the seed), recording it in the audit log in the same
// transaction. Nothing changes, and nothing is recorded, if the holder already
// holds the role at that scope.
func EnsureSystem(tx *gorm.DB, accountID, roleID uint, scope, mode string) (bool, error) {
	g := &models.Grant{
		HolderKind:    models.GrantHolderAccount,
		HolderID:      accountID,
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
	// Never the caller's map: one request may record several entries.
	target := models.AuditTarget{}
	for k, v := range entry.Target {
		target[k] = v
	}
	entry.Target = target
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
