package grants

import (
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EnsureImport gives a newly created mailbox its default import grant and
// audit entry atomically, within the caller's transaction when there is one,
// as EnsureReceive does for receiving. Never call this on startup for an
// existing mailbox: a removed grant must stay removed.
func EnsureImport(db *gorm.DB, mailbox *models.Mailbox) error {
	return ensureOwn(db, mailbox, models.RoleImporter)
}

// EnsureDefaults gives a newly created mailbox every revocable grant it holds
// by default: receiving and importing.
func EnsureDefaults(db *gorm.DB, mailbox *models.Mailbox) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := EnsureReceive(tx, mailbox); err != nil {
			return err
		}
		return EnsureImport(tx, mailbox)
	})
}

// EnsureImportRole creates the protected importer role holding mail:import,
// once. It grants nothing: accounts get it as they are created (EnsureImport).
func EnsureImportRole(db *gorm.DB) error {
	return ensureOneCapabilityRole(db, models.RoleImporter, "Store a message in a mailbox without running delivery rules", models.CapMailImport)
}

// ensureOneCapabilityRole creates a protected role holding one capability. The
// role's existence is the marker: restarts never put back a capability someone
// removed from it.
func ensureOneCapabilityRole(db *gorm.DB, name, description, capabilityName string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		role := models.Role{Name: name, Description: description, SystemRole: true}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&role)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		var capability models.Capability
		if err := tx.Where("name = ?", capabilityName).First(&capability).Error; err != nil {
			return err
		}
		return tx.Create(&models.RoleCapability{RoleID: role.ID, CapabilityID: capability.ID}).Error
	})
}

// ensureOwn gives mailbox the named system role on its own mailbox.
func ensureOwn(db *gorm.DB, mailbox *models.Mailbox, roleName string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var role models.Role
		if err := tx.Where("name = ?", roleName).First(&role).Error; err != nil {
			return err
		}
		_, err := EnsureSystem(tx, mailbox.ID, role.ID, Mailbox(mailbox.Address), models.GrantActive)
		return err
	})
}
