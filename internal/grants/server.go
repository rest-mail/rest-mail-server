package grants

import (
	"fmt"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EnsureServerCeiling migrates the installation-wide server RBAC object into
// the existing role/capability/grant tables. Bootstrap calls it after seeding
// capabilities on both fresh and existing installations. The protected role
// is the initialization marker: restarts never restore removed capabilities,
// even when the set is empty or its grant has been removed.
func EnsureServerCeiling(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := Lock(tx); err != nil {
			return err
		}
		role := models.Role{Name: models.RoleServer, Description: "Installation-wide capability ceiling", SystemRole: true}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&role)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		// An explicit set allows each capability to be removed independently.
		// A wildcard must never make removing one of these rows ineffective.
		if err := tx.Exec(`INSERT INTO role_capabilities (role_id, capability_id)
			SELECT ?, id FROM capabilities WHERE name <> ?`, role.ID, Wildcard).Error; err != nil {
			return fmt.Errorf("initialize server capabilities: %w", err)
		}
		return tx.Create(&models.Grant{
			HolderKind: models.GrantHolderServer, HolderID: models.ServerHolderID,
			RoleID: role.ID, Scope: All, Mode: models.GrantActive,
			GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now(),
		}).Error
	})
}

// withinServer applies the installation's ceiling in the shared authorization
// path. Only the global wildcard is superadmin: a wildcard scoped to one
// domain or mailbox is still capped. Missing/empty grants hold nothing;
// database failures are errors, never permission to use cached server state.
func withinServer(db *gorm.DB, held Held, now time.Time) (Held, error) {
	if held.Allows(Wildcard, All) {
		return held, nil
	}
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
	return held.Within(HeldOf(entries...)), nil
}
