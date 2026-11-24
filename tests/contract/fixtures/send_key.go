package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

func prepareSendKeyRequest(database *gorm.DB, domain string, input io.Reader) error {
	var request struct {
		Address string `json:"address"`
		Key     string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(input, 4096)).Decode(&request); err != nil {
		return err
	}
	return prepareSendKeyProbe(database, domain, request.Address, request.Key)
}

func prepareSendKeyProbe(database *gorm.DB, domain, address, key string) error {
	if domain != "restmail.test" || key == "" || len(key) > 255 {
		return errors.New("send key probes require restmail.test and a key of 1 to 255 bytes")
	}
	switch address {
	case "admin@restmail.test", mfaAdminAddress, probeAdminAddress:
	default:
		return errors.New("send key probes require a disposable contract administrator")
	}
	var account models.Mailbox
	if err := database.Where("address = ?", address).First(&account).Error; err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(key))
	return database.Where("account_id = ? AND key_hash = ?", account.ID, fmt.Sprintf("%x", digest)).
		Delete(&models.SendIdempotency{}).Error
}
