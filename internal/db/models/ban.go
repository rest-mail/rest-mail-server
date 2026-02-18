package models

import "time"

// Ban represents a persistent IP ban managed by administrators.
// Gateway services check this table on connection to enforce bans.
type Ban struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	IP        string     `gorm:"size:45;not null;uniqueIndex" json:"ip"`
	Reason    string     `gorm:"type:text" json:"reason"`
	Protocol  string     `gorm:"size:10;not null;default:all" json:"protocol"` // "smtp", "imap", "pop3", "all"
	CreatedBy string     `gorm:"size:255" json:"created_by"`                   // admin email or "auto"
	ExpiresAt *time.Time `gorm:"index" json:"expires_at,omitempty"`            // nil = permanent
	CreatedAt time.Time  `json:"created_at"`
}

func (Ban) TableName() string { return "bans" }
