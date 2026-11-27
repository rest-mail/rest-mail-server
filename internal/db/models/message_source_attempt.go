package models

import "time"

// MessageSourceAttempt is an independently renewed owner of a source while an
// attempt is live. It can outlast the queue row or that row's current claim.
type MessageSourceAttempt struct {
	ID        string    `gorm:"primaryKey"`
	SourceRef string    `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
}
