package models

import "time"

// SendLimitValues are fixed UTC hour/day ceilings. Zero is unlimited. A missing
// account policy inherits its owner's values, or the configured account defaults.
type SendLimitValues struct {
	MessagesPerHour   int64 `json:"messages_per_hour" minimum:"0"`
	MessagesPerDay    int64 `json:"messages_per_day" minimum:"0"`
	RecipientsPerHour int64 `json:"recipients_per_hour" minimum:"0"`
	RecipientsPerDay  int64 `json:"recipients_per_day" minimum:"0"`
}

// Policies live separately from mailboxes so storage quotas and send limits can
// evolve independently. Scope is account:<id>, domain:<id>, or * (the server).
type SendLimitPolicy struct {
	Scope           string `gorm:"primaryKey;size:80"`
	SendLimitValues `gorm:"embedded"`
	UpdatedAt       time.Time
}

type SendLimitCounter struct {
	Scope         string    `gorm:"primaryKey;size:80"`
	WindowSeconds int       `gorm:"primaryKey"`
	Start         time.Time `gorm:"primaryKey"`
	Messages      int64     `gorm:"not null;default:0"`
	Recipients    int64     `gorm:"not null;default:0"`
}

// One inbox notice per owner per UTC hour coalesces alerts without losing any
// individual hit from the audit log. No message content is copied into a notice.
type SendLimitNotice struct {
	OwnerID    uint      `gorm:"primaryKey"`
	Start      time.Time `gorm:"primaryKey"`
	MessageID  uint
	NotifiedAt *time.Time `gorm:"index"`
}
