package repositories

import (
	"errors"
	"fmt"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
)

// ErrMailboxUnavailable is a known but unusable identity. It is also a
// not-found error for authentication, but delivery must not try an alias or
// plus-address fallback that could redirect it to another account.
var ErrMailboxUnavailable = fmt.Errorf("mailbox identity unavailable: %w", gorm.ErrRecordNotFound)

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
	// Restrict legacy normalization to the hosted domain (domain_id is indexed)
	// rather than scanning all accounts for each unknown UTF-8 recipient.
	var matches []models.Mailbox
	if err := db.Where("domain_id IN (?)", db.Model(&models.Domain{}).Select("id").Where("name = ?", domain)).
		Where("normalize(address, NFC) = ?", canonical).Limit(2).Find(&matches).Error; err != nil {
		return err
	}
	if len(matches) > 1 {
		return ErrMailboxUnavailable
	}
	if len(matches) == 0 {
		return gorm.ErrRecordNotFound
	}
	*mailbox = matches[0]
	return nil
}

// FindActiveMailboxByAddress resolves identity before checking activity. Filtering
// out inactive rows during resolution could redirect an exact legacy spelling to
// a different, active NFC-equivalent account.
func FindActiveMailboxByAddress(db *gorm.DB, address string, mailbox *models.Mailbox) error {
	if rmail.ContainsControlChar(address) {
		return gorm.ErrRecordNotFound
	}
	if err := FindMailboxByAddress(db, address, mailbox); err != nil {
		return err
	}
	if !mailbox.Active {
		return ErrMailboxUnavailable
	}
	return nil
}

// RequireMailboxReceive hides an account without the effective receive grant
// like an unknown identity. It must run before any inbound filter can act.
func RequireMailboxReceive(db *gorm.DB, mailbox *models.Mailbox) error {
	allowed, err := grants.CanReceive(db, mailbox)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrMailboxUnavailable
	}
	return nil
}

// FindReceivingMailboxByAddress resolves identity before checking receive
// grants. A known nonreceiving identity blocks alias/subaddress fallbacks.
func FindReceivingMailboxByAddress(db *gorm.DB, address string, mailbox *models.Mailbox) error {
	if err := FindActiveMailboxByAddress(db, address, mailbox); err != nil {
		return err
	}
	return RequireMailboxReceive(db, mailbox)
}
