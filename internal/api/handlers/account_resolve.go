package handlers

import (
	"fmt"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// resolveAccountMailbox maps a webmail account id from the URL to the mailbox id
// the authenticated user is allowed to act on, enforcing ownership. The user may
// reach either their own account (its primary mailbox) or an account they have
// explicitly linked (via LinkedAccount). Returns an error — never another user's
// mailbox — when neither holds.
//
// This is the single source of truth for /accounts/{id}/... resolution. The
// message, event, and vacation handlers previously each had their own copy, and
// the event/vacation copies matched a linked account by LinkedAccount.id ==
// accountID (treating the webmail-account id as a linked-account id), which
// resolved the wrong mailbox or none for linked accounts.
func resolveAccountMailbox(db *gorm.DB, accountID, webmailAccountID uint) (uint, error) {
	var account models.WebmailAccount
	if err := db.First(&account, accountID).Error; err != nil {
		return 0, fmt.Errorf("account not found or access denied")
	}

	// The user's own account.
	if account.ID == webmailAccountID {
		return account.PrimaryMailboxID, nil
	}

	// An account the user has linked (matched by the target account's mailbox).
	var linked models.LinkedAccount
	if err := db.Where("webmail_account_id = ? AND mailbox_id = ?", webmailAccountID, account.PrimaryMailboxID).
		First(&linked).Error; err == nil {
		return linked.MailboxID, nil
	}

	return 0, fmt.Errorf("account not found or access denied")
}
