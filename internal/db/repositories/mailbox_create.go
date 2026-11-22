package repositories

import (
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

func CreateMailbox(tx *gorm.DB, account *models.Mailbox) error {
	if err := tx.Create(account).Error; err != nil {
		return err
	}
	return tx.Create(&models.QuotaUsage{MailboxID: account.ID}).Error
}
