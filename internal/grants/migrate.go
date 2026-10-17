package grants

import (
	"fmt"
	"log/slog"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// migrateLockKey serialises Migrate across replicas starting at once: "grantmig".
const migrateLockKey int64 = 0x6772616e746d6967

// Migrate creates the grants table, and their activations', and, the one time
// it creates grants, turns each role assignment of admin_users_roles into an
// active grant of that role at "*", each recorded in the audit log, all in one
// transaction: the server either comes up with every assignment it had as a
// grant, or not at all. Every admin keeps exactly the access they had. Adding
// activations to a server that already has grants changes no grant.
//
// Run it after db.AutoMigrate (which creates admin_users_roles and the audit
// log) on every start: once the table exists it only adds what a newer model
// needs, and never copies again, so a grant removed later is not brought
// back by the next start. It returns how many assignments it copied.
func Migrate(db *gorm.DB) (int, error) {
	copied := 0
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", migrateLockKey).Error; err != nil {
			return err
		}
		fresh := !tx.Migrator().HasTable(&models.Grant{})
		if err := tx.AutoMigrate(&models.Grant{}, &models.GrantActivation{}); err != nil {
			return err
		}
		if !fresh || !tx.Migrator().HasTable(&models.UserRole{}) {
			return nil
		}
		var assignments []models.UserRole
		if err := tx.Order("user_id, role_id").Find(&assignments).Error; err != nil {
			return err
		}
		for _, a := range assignments {
			g := &models.Grant{
				HolderKind:    models.GrantHolderAdmin,
				HolderID:      a.UserID,
				RoleID:        a.RoleID,
				Scope:         All,
				Mode:          models.GrantActive,
				GrantedByKind: models.AuditActorSystem,
				GrantedAt:     a.GrantedAt,
			}
			if a.GrantedBy != nil {
				g.GrantedByKind, g.GrantedByID = models.AuditActorAdmin, a.GrantedBy
			}
			if err := tx.Omit(clause.Associations).Create(g).Error; err != nil {
				return err
			}
			entry := models.AuditEntry{
				ActorKind: models.AuditActorSystem,
				ActorName: "system",
				Action:    ActionMigrate,
			}
			if err := appendChange(tx, g, entry); err != nil {
				return err
			}
			copied++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("grants migration failed: %w", err)
	}
	if copied > 0 {
		slog.Info("role assignments became grants at \"*\"", "count", copied)
	}
	return copied, nil
}
