package models

import "time"

type Domain struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	Name              string    `gorm:"size:255;not null;uniqueIndex" json:"name"`
	ServerType        string    `gorm:"size:20;not null;default:traditional" json:"server_type"` // 'traditional' or 'restmail'
	Active            bool      `gorm:"default:true" json:"active"`
	DefaultQuotaBytes int64     `gorm:"default:1073741824" json:"default_quota_bytes"` // 1GB
	DKIMSelector      string    `gorm:"size:63" json:"dkim_selector"`
	DKIMPrivateKey    string    `gorm:"type:text" json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`

	// Associations
	Mailboxes []Mailbox `gorm:"foreignKey:DomainID" json:"mailboxes,omitempty"`
	Aliases   []Alias   `gorm:"foreignKey:DomainID" json:"aliases,omitempty"`
}
