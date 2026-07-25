package smtp

import (
	"errors"
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

// PersistSubmittedMessage records an authenticated submission as a message row
// owned by the sender's mailbox (folder "Sent") and returns its id, so an
// eventual bounce/DSN can be authenticated back to the submitting mailbox. When
// the sender has no local mailbox there is nothing to attribute the message to,
// so it returns (nil, nil) rather than an error.
func (s *dbStore) PersistSubmittedMessage(msg SubmittedMessage) (*uint, error) {
	var mb models.Mailbox
	if err := s.db.Where("address = ? AND active = ?", msg.Sender, true).First(&mb).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	threadID := msg.MessageID
	if msg.InReplyTo != "" {
		threadID = msg.InReplyTo
	}
	senderName := msg.SenderName
	if senderName == "" {
		senderName = mb.DisplayName
	}

	row := models.Message{
		MailboxID:    mb.ID,
		Folder:       "Sent",
		MsgID:        msg.MessageID,
		InReplyTo:    msg.InReplyTo,
		References:   msg.References,
		ThreadID:     threadID,
		Sender:       msg.Sender,
		SenderName:   senderName,
		RecipientsTo: models.JSONB(msg.RecipientsTo),
		RecipientsCc: models.JSONB(msg.RecipientsCc),
		Subject:      msg.Subject,
		BodyText:     msg.BodyText,
		BodyHTML:     msg.BodyHTML,
		RawMessage:   msg.RawMessage,
		IsRead:       true,
		SizeBytes:    len(msg.Subject) + len(msg.BodyText) + len(msg.BodyHTML),
		RawSize:      len(msg.RawMessage),
		ReceivedAt:   time.Now(),
	}
	if err := s.db.Create(&row).Error; err != nil {
		return nil, err
	}
	return &row.ID, nil
}

// EnqueueOutbound inserts the message into the outbound queue with the same
// pending/retry/expiry defaults the session used to set inline.
func (s *dbStore) EnqueueOutbound(msg OutboundMessage) error {
	entry := models.OutboundQueue{
		MessageID:  msg.MessageID,
		Sender:     msg.Sender,
		Recipient:  msg.Recipient,
		Domain:     msg.Domain,
		RawMessage: msg.RawMessage,
		BodyType:   msg.BodyType,
		Status:     "pending",
		MaxRetries: 30,
		ExpiresAt:  time.Now().Add(72 * time.Hour),
	}
	return s.db.Create(&entry).Error
}
