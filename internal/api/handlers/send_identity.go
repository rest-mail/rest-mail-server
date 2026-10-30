package handlers

import (
	"errors"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// maySendFrom resolves the represented mailbox before choosing a capability.
// Read/write and the legacy mail:send grant on someone else's mailbox grant
// neither delegated identity. mayReach also enforces the credential's scope.
func maySendFrom(db *gorm.DB, claims *auth.Claims, address string, onBehalf bool) (*models.Mailbox, error) {
	var mailbox models.Mailbox
	if err := repositories.FindMailboxByAddress(db, address, &mailbox); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errNoAccess
		}
		return nil, err
	}
	capability := models.CapMailSendAs
	if onBehalf {
		capability = models.CapMailSendOnBehalf
	} else if mailbox.ID == claims.AccountID {
		capability = models.CapMailSend
	}
	return mayReach(db, claims, mailbox.ID, capability)
}
