package smtp

import (
	"errors"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	rmail "github.com/restmail/restmail/internal/mail"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
)

// dbStore is the production Store: it reaches the database directly for sender
// authorization and outbound queueing. It exists so those two direct-DB concerns
// live outside the SMTP session, which now depends only on the Store interface
// and no longer imports gorm or the DB models.
type dbStore struct {
	db *gorm.DB
	// masterKey decrypts a domain's at-rest DKIM key, as the queue worker's
	// does (Worker.SetMasterKey). Empty only when encryption is disabled (dev).
	masterKey string
}

// NewStore returns a database-backed Store for the SMTP gateway.
func NewStore(db *gorm.DB, masterKey string) Store {
	return &dbStore{db: db, masterKey: masterKey}
}

func (s *dbStore) SenderMatchesAccount(accountID uint, from string) (bool, error) {
	var mb models.Mailbox
	if err := repositories.FindMailboxByAddress(s.db, from, &mb); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return mb.ID == accountID, nil
}

// SenderAuthorized reads the account's grants, as the API's send path does:
// the account may send as from when from is an account it holds mail:send on.
// from is resolved to its hosted identity first, so a Unicode domain or
// NFC-equivalent spelling is checked against the grant on the stored address.
func (s *dbStore) SenderAuthorized(accountID uint, from string) (bool, error) {
	var mb models.Mailbox
	if err := repositories.FindMailboxByAddress(s.db, from, &mb); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	held, err := grants.Load(s.db, accountID, time.Now())
	if err != nil {
		return false, err
	}
	return held.Allows(models.CapMailSend, grants.Mailbox(mb.Address)), nil
}

// AccountMaySend reads the account's grants as SenderAuthorized does, for its
// own mailbox. grants.Load applies the server's ceiling live (and exempts only
// the global superadmin), so nothing here is cached between messages.
func (s *dbStore) AccountMaySend(accountID uint) (bool, error) {
	var mb models.Mailbox
	if err := s.db.Select("id", "address").Where("id = ? AND active", accountID).Take(&mb).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	held, err := grants.Load(s.db, accountID, time.Now())
	if err != nil {
		return false, err
	}
	return held.Allows(models.CapMailSend, grants.Mailbox(mb.Address)), nil
}

// PersistSubmittedMessage records an authenticated submission as a message row
// owned by the sender's mailbox (folder "Sent") and returns its id, so an
// eventual bounce/DSN can be authenticated back to the submitting mailbox. When
// the sender has no local mailbox there is nothing to attribute the message to,
// so it returns (nil, nil) rather than an error.
func (s *dbStore) PersistSubmittedMessage(msg SubmittedMessage) (*uint, error) {
	var mb models.Mailbox
	if err := repositories.FindActiveMailboxByAddress(s.db, msg.Sender, &mb); err != nil {
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

// CredentialUsable checks the credentials table directly, as SenderAuthorized
// reads grants.
func (s *dbStore) CredentialUsable(credentialID uint) (bool, error) {
	return repositories.NewCredentialRepository(s.db).Usable(credentialID, time.Now())
}

// PrepareSubmitted runs the transforms the queue worker runs on the same
// submission's remote copies, so a local mailbox receives what a remote one
// does.
func (s *dbStore) PrepareSubmitted(sender, raw string) (string, error) {
	return outbound.Prepare(s.db, s.masterKey, rmail.DomainFromAddress(sender), raw)
}

// SendingPolicy reads the sending policy of the domain the address is on.
func (s *dbStore) SendingPolicy(address string) (string, error) {
	var policies []string
	err := s.db.Model(&models.Domain{}).
		Where("name = ?", strings.ToLower(rmail.DomainFromAddress(address))).
		Pluck("sending_policy", &policies).Error
	if err != nil || len(policies) == 0 {
		return "", err
	}
	return policies[0], nil
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
		RequireTLS: msg.RequireTLS,
		Status:     "pending",
		MaxRetries: models.DefaultMaxRetries,
		ExpiresAt:  time.Now().Add(models.QueueLifetime),
	}
	return s.db.Create(&entry).Error
}
