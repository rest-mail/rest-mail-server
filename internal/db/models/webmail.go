package models

import "time"

type WebmailAccount struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	PrimaryMailboxID uint      `gorm:"not null;uniqueIndex" json:"primary_mailbox_id"`
	IsAdmin          bool      `gorm:"default:false" json:"is_admin"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	// Associations
	PrimaryMailbox Mailbox         `gorm:"foreignKey:PrimaryMailboxID" json:"primary_mailbox,omitempty"`
	LinkedAccounts []LinkedAccount `gorm:"foreignKey:WebmailAccountID" json:"linked_accounts,omitempty"`
}

func (WebmailAccount) TableName() string { return "webmail_accounts" }

type LinkedAccount struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// WebmailAccountID is plainly indexed for lookup. The uniqueness that prevents
	// double-linking lives on MailboxID alone (below): a mailbox may be linked to at
	// most one webmail account, so it cannot be claimed by two accounts (OSI-21).
	WebmailAccountID uint `gorm:"not null;index" json:"webmail_account_id"`
	// MailboxID carries a standalone unique index (idx_linked_accounts_mailbox): the
	// backstop that makes concurrent link attempts for the same mailbox conflict at
	// the database rather than racing to create duplicate/inconsistent linkage.
	MailboxID   uint      `gorm:"not null;uniqueIndex:idx_linked_accounts_mailbox" json:"mailbox_id"`
	DisplayName string    `gorm:"size:255" json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`

	// Associations
	WebmailAccount WebmailAccount `gorm:"foreignKey:WebmailAccountID" json:"webmail_account,omitempty"`
	Mailbox        Mailbox        `gorm:"foreignKey:MailboxID" json:"mailbox,omitempty"`
}

func (LinkedAccount) TableName() string { return "linked_accounts" }
