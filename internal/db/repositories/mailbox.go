package repositories

import (
	"errors"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
)

// FindMailboxByAddress resolves a hosted account identity. Exact legacy
// spellings take precedence: NFC-equivalent accounts may already coexist in
// older databases, and must never be silently redirected to each other.
// Otherwise a unique NFC equivalent is accepted without rewriting stored
// addresses or their grants. Ambiguous equivalents are treated as not found.
func FindMailboxByAddress(db *gorm.DB, address string, mailbox *models.Mailbox) error {
	canonical, err := rmail.CanonicalMailboxAddress(address)
	if err != nil {
		return gorm.ErrRecordNotFound
	}
	local, domain, _ := strings.Cut(canonical, "@")
	legacyLocal, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(address)), "@")
	legacy := legacyLocal + "@" + domain
	err = db.Where("address = ?", legacy).Take(mailbox).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if canonical != legacy {
		err = db.Where("address = ?", canonical).Take(mailbox).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	// ASCII local parts cannot have decomposed NFC equivalents. Keep the
	// common (including unknown ASCII account) path on the address index.
	if strings.IndexFunc(local, func(r rune) bool { return r > 127 }) < 0 {
		return gorm.ErrRecordNotFound
	}
	var matches []models.Mailbox
	if err := db.Where("normalize(address, NFC) = ?", canonical).Limit(2).Find(&matches).Error; err != nil {
		return err
	}
	if len(matches) != 1 {
		return gorm.ErrRecordNotFound
	}
	*mailbox = matches[0]
	return nil
}
