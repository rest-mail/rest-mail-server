package grants

import (
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// CanReceive is the mandatory inbound RBAC gate, before reports, filters or
// delivery side effects. It uses live account, owner and server grants.
func CanReceive(db *gorm.DB, mailbox *models.Mailbox) (bool, error) {
	if !mailbox.Active {
		return false, nil
	}
	held, err := Load(db, mailbox.ID, time.Now())
	if err != nil {
		return false, err
	}
	return held.Allows(models.CapMailReceive, Mailbox(mailbox.Address)), nil
}

// EnsureReceive gives a newly created mailbox its default receive grant and
// audit entry atomically, within the caller's transaction when there is one.
// Never call this on startup for an existing mailbox: a removed grant must
// stay removed.
func EnsureReceive(db *gorm.DB, mailbox *models.Mailbox) error {
	return ensureOwn(db, mailbox, models.RoleReceiver)
}

// EnsureReceiverRole creates the protected receiver role holding mail:receive,
// once. It grants nothing: accounts get it as they are created (EnsureReceive).
func EnsureReceiverRole(db *gorm.DB) error {
	return ensureOneCapabilityRole(db, models.RoleReceiver, "Accept inbound mail for a mailbox", models.CapMailReceive)
}
