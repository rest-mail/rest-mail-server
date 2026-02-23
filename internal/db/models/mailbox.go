package models

import "time"

type Mailbox struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	DomainID       uint      `gorm:"not null;index" json:"domain_id"`
	LocalPart      string    `gorm:"size:64;not null" json:"local_part"`
	Address        string    `gorm:"size:255;not null;uniqueIndex" json:"address"`
	Password       string    `gorm:"size:255;not null" json:"-"` // {BLF-CRYPT} bcrypt hash
	DisplayName    string    `gorm:"size:255" json:"display_name"`
	QuotaBytes     int64     `gorm:"default:1073741824" json:"quota_bytes"`
	QuotaUsedBytes int64     `gorm:"default:0" json:"quota_used_bytes"`
	Active         bool      `gorm:"default:true" json:"active"`
	LastLoginAt    *time.Time `json:"last_login_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	// Associations
	Domain     Domain      `gorm:"foreignKey:DomainID" json:"domain,omitempty"`
	Messages   []Message   `gorm:"foreignKey:MailboxID" json:"messages,omitempty"`
	QuotaUsage *QuotaUsage `gorm:"foreignKey:MailboxID" json:"quota_usage,omitempty"`
}

// UniqueIndex on (domain_id, local_part)
func (Mailbox) TableName() string { return "mailboxes" }
