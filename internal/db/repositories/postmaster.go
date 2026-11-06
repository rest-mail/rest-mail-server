package repositories

import (
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

func CreateDomainPostmaster(tx *gorm.DB, domain models.Domain) (*models.Mailbox, error) {
	if err := grants.Lock(tx); err != nil {
		return nil, err
	}
	account := models.Mailbox{
		DomainID: domain.ID, LocalPart: "postmaster", Address: "postmaster@" + domain.Name,
		Active: true, QuotaBytes: domain.DefaultQuotaBytes,
	}
	if err := tx.Create(&account).Error; err != nil {
		return nil, err
	}
	if domain.DefaultQuotaBytes == 0 {
		if err := tx.Model(&account).Update("quota_bytes", 0).Error; err != nil {
			return nil, err
		}
	}
	if err := tx.Create(&models.QuotaUsage{MailboxID: account.ID}).Error; err != nil {
		return nil, err
	}
	if err := grants.EnsureReceive(tx, &account); err != nil {
		return nil, err
	}
	return &account, nil
}
