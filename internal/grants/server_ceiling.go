package grants

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// ActionServerCapabilitiesUpdate is the audit action of a change to the
// stored server capability ceiling.
const ActionServerCapabilitiesUpdate = "server_capabilities.update"

var (
	// ErrSuperadminRequired: only a holder of every capability at "*" may read
	// or change the server ceiling.
	ErrSuperadminRequired = errors.New("only a superadmin can read or change the server capability ceiling")
	// ErrServerOverridden: RESTMAIL_SERVER_CAPABILITIES owns the ceiling, so a
	// stored change would have no effect until the variable is removed.
	ErrServerOverridden = errors.New("the server capability ceiling is set by " + ServerCapabilitiesEnv + "; change it there and restart, or unset it to manage the ceiling here")
)

// InvalidCapabilityError names a capability refused for the server ceiling:
// not in the vocabulary, empty, or the wildcard.
type InvalidCapabilityError struct {
	Reason string
}

func (e *InvalidCapabilityError) Error() string { return e.Reason }

// RequireSuperadmin checks, in tx, that accountID holds every capability at
// "*" now. ErrSuperadminRequired when it does not.
func RequireSuperadmin(tx *gorm.DB, accountID uint) error {
	held, err := Load(tx, accountID, time.Now())
	if err != nil {
		return err
	}
	if !held.Allows(Wildcard, All) {
		return ErrSuperadminRequired
	}
	return nil
}

// ServerCeiling is the ceiling in force, sorted: the operator override when
// configured (overridden is then true), otherwise the stored server object.
func ServerCeiling(db *gorm.DB, now time.Time) (names []string, overridden bool, err error) {
	if override := serverOverride.Load(); override != nil {
		return sortedCapabilities(*override), true, nil
	}
	stored, err := storedServerCeiling(db, now)
	if err != nil {
		return nil, false, err
	}
	return sortedCapabilities(stored), false, nil
}

// SetServerCeiling replaces the stored server ceiling with names and records
// the change, with the set before and after, in the audit log in tx. The
// caller holds Lock and has checked RequireSuperadmin. Names are checked as
// RESTMAIL_SERVER_CAPABILITIES is (*InvalidCapabilityError); an empty set
// denies every ordinary capability. ErrServerOverridden while the override is
// configured.
func SetServerCeiling(tx *gorm.DB, names []string, entry models.AuditEntry) ([]string, error) {
	if serverOverride.Load() != nil {
		return nil, ErrServerOverridden
	}
	var known []string
	if err := tx.Model(&models.Capability{}).Where("name <> ?", Wildcard).Pluck("name", &known).Error; err != nil {
		return nil, fmt.Errorf("read capability vocabulary: %w", err)
	}
	vocabulary := map[string]bool{}
	for _, name := range known {
		vocabulary[name] = true
	}
	wanted := map[string]bool{}
	for _, name := range names {
		if err := checkServerCapability(strings.TrimSpace(name), vocabulary); err != nil {
			return nil, &InvalidCapabilityError{Reason: err.Error()}
		}
		wanted[strings.TrimSpace(name)] = true
	}
	after := make([]string, 0, len(wanted))
	for name := range wanted {
		after = append(after, name)
	}
	sort.Strings(after)

	now := time.Now()
	stored, err := storedServerCeiling(tx, now)
	if err != nil {
		return nil, err
	}
	before := sortedCapabilities(stored)

	var role models.Role
	if err := tx.Where("name = ?", models.RoleServer).First(&role).Error; err != nil {
		return nil, fmt.Errorf("find the server role: %w", err)
	}
	if err := tx.Where("role_id = ?", role.ID).Delete(&models.RoleCapability{}).Error; err != nil {
		return nil, err
	}
	if len(after) > 0 {
		if err := tx.Exec(`INSERT INTO role_capabilities (role_id, capability_id)
			SELECT ?, id FROM capabilities WHERE name IN ?`, role.ID, after).Error; err != nil {
			return nil, err
		}
	}
	// The set only bounds anyone through an active, unexpiring server grant:
	// restore it if it was removed or altered outside the API.
	res := tx.Model(&models.Grant{}).
		Where("holder_kind = ? AND holder_id = ? AND role_id = ? AND scope = ?", models.GrantHolderServer, models.ServerHolderID, role.ID, All).
		Updates(map[string]interface{}{"mode": models.GrantActive, "expires_at": nil})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if err := tx.Create(&models.Grant{
			HolderKind: models.GrantHolderServer, HolderID: models.ServerHolderID,
			RoleID: role.ID, Scope: All, Mode: models.GrantActive,
			GrantedByKind: entry.ActorKind, GrantedByID: entry.ActorID, GrantedAt: now,
		}).Error; err != nil {
			return nil, err
		}
	}

	entry.Action = ActionServerCapabilitiesUpdate
	entry.Outcome = models.AuditOutcomeSuccess
	target := models.AuditTarget{}
	for k, v := range entry.Target {
		target[k] = v
	}
	target["before"] = strings.Join(before, ",")
	target["after"] = strings.Join(after, ",")
	target["added"] = strings.Join(difference(after, before), ",")
	target["removed"] = strings.Join(difference(before, after), ",")
	entry.Target = target
	if err := audit.Append(tx, &entry); err != nil {
		return nil, fmt.Errorf("record the server ceiling change in the audit log: %w", err)
	}
	return after, nil
}

// storedServerCeiling is the server object's capability set in the database,
// through its active, unexpired grant at "*".
func storedServerCeiling(db *gorm.DB, now time.Time) (Held, error) {
	var entries []Entry
	err := db.Table("grants AS g").
		Select("g.scope, c.name AS capability").
		Joins("JOIN role_capabilities rc ON rc.role_id = g.role_id").
		Joins("JOIN capabilities c ON c.id = rc.capability_id").
		Where("g.holder_kind = ? AND g.holder_id = ? AND g.scope = ?", models.GrantHolderServer, models.ServerHolderID, All).
		Where("g.mode = ? AND (g.expires_at IS NULL OR g.expires_at > ?)", models.GrantActive, now).
		Where("c.name <> ?", Wildcard).
		Scan(&entries).Error
	if err != nil {
		return Held{}, err
	}
	return HeldOf(entries...), nil
}

// checkServerCapability refuses a name the server ceiling cannot hold: empty,
// the wildcard, or outside the capability vocabulary.
func checkServerCapability(name string, vocabulary map[string]bool) error {
	switch {
	case name == "":
		return errors.New("empty capability name")
	case name == Wildcard:
		return errors.New("the wildcard is not allowed; list capabilities explicitly")
	case !vocabulary[name]:
		return fmt.Errorf("unknown capability %q", name)
	}
	return nil
}

// sortedCapabilities is each capability h holds, once, in order.
func sortedCapabilities(h Held) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range h.entries {
		if !seen[e.Capability] {
			seen[e.Capability] = true
			out = append(out, e.Capability)
		}
	}
	sort.Strings(out)
	return out
}

// difference is each name in a that is not in b, in a's order.
func difference(a, b []string) []string {
	in := map[string]bool{}
	for _, name := range b {
		in[name] = true
	}
	var out []string
	for _, name := range a {
		if !in[name] {
			out = append(out, name)
		}
	}
	return out
}
