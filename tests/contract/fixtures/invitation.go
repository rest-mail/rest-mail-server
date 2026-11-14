package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func prepareInvitationProbe(database *gorm.DB, domain, token string) error {
	if domain != "restmail.test" || strings.TrimSpace(token) == "" || len(token) > 1024 {
		return errors.New("invitation probes require restmail.test and a nonempty disposable token")
	}
	return database.Transaction(func(tx *gorm.DB) error {
		var hosted models.Domain
		if err := tx.Where("name = ?", domain).First(&hosted).Error; err != nil {
			return err
		}
		local := fmt.Sprintf("contract-invitation-%d", time.Now().UnixNano())
		account := models.Mailbox{DomainID: hosted.ID, LocalPart: local, Address: local + "@" + domain, Active: true}
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(token))
		invitation := models.OneTimeToken{
			Purpose: models.OneTimeTokenInvitation, TokenHash: hex.EncodeToString(digest[:]),
			AccountID: &account.ID, ExpiresAt: time.Now().Add(bootstrap.DefaultTokenLifetime),
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "token_hash"}},
			DoUpdates: clause.AssignmentColumns([]string{"purpose", "account_id", "expires_at", "used_at"}),
		}).Create(&invitation).Error
	})
}
