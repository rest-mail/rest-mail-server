package smtp

import (
	"context"
	"errors"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/sendlimits"
)

// SubmissionBudget is implemented by the production DB store. A temporary
// refusal transfers no delivery responsibility: SMTP's submitting client keeps
// the message, avoiding a server-held copy being released and then sent twice
// when that client retries its 451. An inactive identity is
// sendlimits.ErrAccountInactive, which is refused for good: no retry can help.
type SubmissionBudget interface {
	PersistentSendLimits() bool
	ReserveSubmission(SubmissionAttempt) (bool, error)
}
type SubmissionAttempt struct {
	AccountID    uint
	CredentialID uint
	Sender       string
	HeaderSender string
	Recipients   int64
	Remote       string
}

func (s *dbStore) PersistentSendLimits() bool { return s.sendLimits != nil }
func (s *dbStore) ReserveSubmission(a SubmissionAttempt) (bool, error) {
	if s.sendLimits == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := s.db.WithContext(ctx)
	var sender models.Mailbox
	if err := repositories.FindActiveMailboxByAddress(db, a.Sender, &sender); err != nil {
		return false, identityError(err)
	}
	var actor models.Mailbox
	if err := db.Select("address").First(&actor, a.AccountID).Error; err != nil {
		return false, err
	}
	e := &models.AuditEntry{ActorKind: models.AuditActorAccount, ActorID: &a.AccountID, ActorName: actor.Address, Method: "SMTP", Route: "submission/DATA", Status: 451, IP: a.Remote}
	if a.CredentialID != 0 {
		e.CredentialID = &a.CredentialID
	}
	var headerID uint
	if a.HeaderSender != "" && a.HeaderSender != a.Sender {
		var header models.Mailbox
		if err := repositories.FindActiveMailboxByAddress(db, a.HeaderSender, &header); err != nil {
			return false, identityError(err)
		}
		headerID = header.ID
	}
	hit, err := s.sendLimits.Reserve(db, sendlimits.Attempt{AccountID: a.AccountID, SenderID: sender.ID, HeaderSenderID: headerID, Recipients: a.Recipients, Entry: e})
	return hit != nil, err
}

// identityError is a failed lookup of an authorized sending identity: a known
// identity that cannot be used is as final as an inactive one in its chain.
func identityError(err error) error {
	if errors.Is(err, repositories.ErrMailboxUnavailable) {
		return sendlimits.ErrAccountInactive
	}
	return err
}
