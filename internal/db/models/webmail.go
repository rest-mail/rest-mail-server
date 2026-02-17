package models

import "time"

type WebmailAccount struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	PrimaryMailboxID uint      `gorm:"not null;uniqueIndex" json:"primary_mailbox_id"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	// Associations
	PrimaryMailbox Mailbox         `gorm:"foreignKey:PrimaryMailboxID" json:"primary_mailbox,omitempty"`
	LinkedAccounts []LinkedAccount `gorm:"foreignKey:WebmailAccountID" json:"linked_accounts,omitempty"`
}

func (WebmailAccount) TableName() string { return "webmail_accounts" }

type LinkedAccount struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	WebmailAccountID uint      `gorm:"not null;index;uniqueIndex:idx_webmail_mailbox" json:"webmail_account_id"`
	MailboxID        uint      `gorm:"not null;uniqueIndex:idx_webmail_mailbox" json:"mailbox_id"`
	DisplayName      string    `gorm:"size:255" json:"display_name"`
	CreatedAt        time.Time `json:"created_at"`

	// Associations
	WebmailAccount WebmailAccount `gorm:"foreignKey:WebmailAccountID" json:"webmail_account,omitempty"`
	Mailbox        Mailbox        `gorm:"foreignKey:MailboxID" json:"mailbox,omitempty"`
}

func (LinkedAccount) TableName() string { return "linked_accounts" }
