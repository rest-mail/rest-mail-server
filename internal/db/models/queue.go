package models

import "time"

type OutboundQueue struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	MessageID     *uint      `gorm:"index" json:"message_id"`
	Sender        string     `gorm:"size:255;not null" json:"sender"`
	Recipient     string     `gorm:"size:255;not null" json:"recipient"`
	Domain        string     `gorm:"size:255;not null;index" json:"domain"` // destination domain for MX lookup
	RawMessage    string     `gorm:"type:text" json:"-"`                   // RFC 2822 formatted message
	Status        string     `gorm:"size:20;not null;default:pending;index" json:"status"`
	// Status values: pending, delivering, deferred, delivered, bounced, expired
	Attempts      int        `gorm:"default:0" json:"attempts"`
	LastAttempt   *time.Time `json:"last_attempt"`
	NextAttempt   time.Time  `gorm:"default:now();index" json:"next_attempt"`
	LastError     string     `gorm:"type:text" json:"last_error"`
	LastErrorCode int        `json:"last_error_code"`
	MaxRetries    int        `gorm:"default:30" json:"max_retries"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`

	// Associations
	Message *Message `gorm:"foreignKey:MessageID" json:"message,omitempty"`
}

func (OutboundQueue) TableName() string { return "outbound_queue" }
