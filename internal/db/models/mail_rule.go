package models

import (
	"time"

	"github.com/rest-mail/rest-mail-server/internal/rules"
)

// MailRule is one native account rule. Exported Sieve is derived, never a second
// script row. Delivery effects retain rule identity and version after deletion.
type MailRule struct {
	ID               uint `gorm:"primaryKey"`
	MailboxID        uint `gorm:"not null;index:idx_mail_rules_account_position"`
	Version          uint `gorm:"not null"`
	rules.Definition `gorm:"embedded"`
	RunCount         uint64 `gorm:"not null;default:0"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (MailRule) TableName() string { return "mail_rules" }
