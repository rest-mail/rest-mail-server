package smtp

import (
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// dbStore is the production Store: it reaches the database directly for sender
// authorization and outbound queueing. It exists so those two direct-DB concerns
// live outside the SMTP session, which now depends only on the Store interface
// and no longer imports gorm or the DB models.
type dbStore struct {
	db *gorm.DB
}

// NewStore returns a database-backed Store for the SMTP gateway.
func NewStore(db *gorm.DB) Store {
	return &dbStore{db: db}
}

// SenderAuthorized runs the same linked_accounts -> mailboxes lookup the session
// used to perform inline: the account is authorized for the address when it owns
// a linked mailbox with that address.
func (s *dbStore) SenderAuthorized(accountID uint, from string) (bool, error) {
	var count int64
	err := s.db.Table("linked_accounts").
		Joins("JOIN mailboxes ON mailboxes.id = linked_accounts.mailbox_id").
		Where("linked_accounts.webmail_account_id = ? AND mailboxes.address = ?", accountID, from).
		Count(&count).Error
	return count > 0, err
}

// EnqueueOutbound inserts the message into the outbound queue with the same
// pending/retry/expiry defaults the session used to set inline.
func (s *dbStore) EnqueueOutbound(msg OutboundMessage) error {
	entry := models.OutboundQueue{
		Sender:     msg.Sender,
		Recipient:  msg.Recipient,
		Domain:     msg.Domain,
		RawMessage: msg.RawMessage,
		Status:     "pending",
		MaxRetries: 30,
		ExpiresAt:  time.Now().Add(72 * time.Hour),
	}
	return s.db.Create(&entry).Error
}
