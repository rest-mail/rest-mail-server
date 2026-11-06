package db

import (
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// MigrateFolderSubscriptions creates the IMAP subscription choices (#1675): one row
// per (mailbox, folder name) the account subscribed or unsubscribed, the pair its
// primary key, gone with its mailbox.
func MigrateFolderSubscriptions(database *gorm.DB) error {
	if err := database.AutoMigrate(&models.FolderSubscription{}); err != nil {
		return err
	}
	if !database.Migrator().HasConstraint(&models.FolderSubscription{}, "Mailbox") {
		return database.Migrator().CreateConstraint(&models.FolderSubscription{}, "Mailbox")
	}
	return nil
}
