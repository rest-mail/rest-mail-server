package grants

import (
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CanReceive is the mandatory inbound RBAC gate, before reports, filters or
// delivery side effects. It uses live account, owner and server grants.
func CanReceive(db *gorm.DB, mailbox *models.Mailbox) (bool, error) {
	if !mailbox.Active {
		return false, nil
	}
	held, err := Load(db, mailbox.ID, time.Now())
	if err != nil {
		return false, err
	}
	return held.Allows(models.CapMailReceive, Mailbox(mailbox.Address)), nil
}

// EnsureReceive gives a newly created mailbox its default receive grant and
// audit entry atomically, within the caller's transaction when there is one.
// Never call this on startup for an existing mailbox: a removed grant must
// stay removed.
func EnsureReceive(db *gorm.DB, mailbox *models.Mailbox) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var role models.Role
		if err := tx.Where("name = ?", models.RoleReceiver).First(&role).Error; err != nil {
			return err
		}
		_, err := EnsureSystem(tx, mailbox.ID, role.ID, Mailbox(mailbox.Address), models.GrantActive)
		return err
	})
}

// EnsureReceiveDefaults backfills the old implicit ability to receive exactly
// once. Creation of the protected receiver role is the migration marker, in
// the same transaction as every grant and audit entry. Restarts never restore
// a receive grant or a server permission that someone removed.
func EnsureReceiveDefaults(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := Lock(tx); err != nil {
			return err
		}
		role := models.Role{Name: models.RoleReceiver, Description: "Accept inbound mail for a mailbox", SystemRole: true}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&role)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		var capability models.Capability
		if err := tx.Where("name = ?", models.CapMailReceive).First(&capability).Error; err != nil {
			return err
		}
		if err := tx.Create(&models.RoleCapability{RoleID: role.ID, CapabilityID: capability.ID}).Error; err != nil {
			return err
		}
		// An already initialized, nonempty server ceiling gains the new
		// vocabulary once, preserving existing inbound behavior. An empty
		// ceiling stays closed; fresh ceilings are initialized afterwards.
		if err := tx.Exec(`INSERT INTO role_capabilities (role_id, capability_id)
			SELECT r.id, ? FROM roles r WHERE r.name = ?
			AND EXISTS (SELECT 1 FROM role_capabilities rc WHERE rc.role_id = r.id)
			ON CONFLICT DO NOTHING`, capability.ID, models.RoleServer).Error; err != nil {
			return err
		}
		var mailboxes []models.Mailbox
		return tx.FindInBatches(&mailboxes, 100, func(batch *gorm.DB, _ int) error {
			for i := range mailboxes {
				if err := EnsureReceive(tx, &mailboxes[i]); err != nil {
					return err
				}
			}
			return nil
		}).Error
	})
}
