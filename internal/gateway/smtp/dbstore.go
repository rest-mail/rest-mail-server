package smtp

import (
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/outbound"
	"github.com/rest-mail/rest-mail-server/internal/sendlimits"
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
	masterKey  string
	sendLimits *sendlimits.Service
}

// NewStore returns a database-backed Store for the SMTP gateway.
func NewStore(db *gorm.DB, masterKey string) Store {
	return &dbStore{db: db, masterKey: masterKey}
}

// NewLimitedStore shares the API's persistent submission budgets.
func NewLimitedStore(db *gorm.DB, masterKey string, limits *sendlimits.Service) Store {
	return &dbStore{db: db, masterKey: masterKey, sendLimits: limits}
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

// HostedDomain looks the domain up as the rest of the server does.
func (s *dbStore) HostedDomain(domain string) (bool, error) {
	return repositories.IsHostedDomain(s.db, domain)
}

// RelayBounce queues the bounce as queueRemoteDSN queues a DSN (#715): a null
// reverse path, marked as a DSN, sent as a hosted domain and held to the same
// per-recipient limit. The limit ignores case, since an SRS address is valid in
// any case and its holder could otherwise pick a new spelling for each message.
func (s *dbStore) RelayBounce(msg BounceRelay) error {
	now := time.Now()
	if msg.MaxPerRecipient > 0 {
		var recent int64
		err := s.db.Model(&models.OutboundQueue{}).
			Where("is_dsn = ? AND LOWER(recipient) = LOWER(?) AND created_at > ?", true, msg.Recipient, now.Add(-msg.Window)).
			Count(&recent).Error
		if err != nil {
			return err
		}
		if recent >= int64(msg.MaxPerRecipient) {
			slog.Warn("smtp: DSN rate limit exceeded for an SRS bounce, dropping it",
				"recipient", msg.Recipient, "recent", recent, "max", msg.MaxPerRecipient, "window", msg.Window)
			return nil
		}
	}
	// The address may arrive in any case, and a domain's key and relay are found by
	// its stored name. It passed HostedDomain, so it has one.
	sendingDomain, err := rmail.DomainASCII(msg.SendingDomain)
	if err != nil {
		return err
	}
	entry := models.OutboundQueue{
		Sender:        "",
		SendingDomain: sendingDomain,
		Recipient:     msg.Recipient,
		Domain:        rmail.DomainFromAddress(msg.Recipient),
		RawMessage:    msg.RawMessage,
		BodyType:      msg.BodyType,
		IsDSN:         true,
		Status:        "pending",
		MaxRetries:    models.DefaultMaxRetries,
		ExpiresAt:     now.Add(models.QueueLifetime),
	}
	return s.db.Create(&entry).Error
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
