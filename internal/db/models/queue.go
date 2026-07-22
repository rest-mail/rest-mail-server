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

	// NOTE: no Message association here, deliberately. It was never used (the
	// queue carries RawMessage inline), and under
	// DisableForeignKeyConstraintWhenMigrating GORM misresolved
	// `Message *Message gorm:"foreignKey:MessageID;references:ID"` as a has-one
	// with the FK matched BY COLUMN NAME to messages.message_id (the RFC 5322
	// Message-ID string, models.Message.MsgID) — and auto-migrated that column
	// to bigint, breaking storage of every inbound message. MessageID above is
	// a plain indexed column; resolve it manually if you need the message.
}

func (OutboundQueue) TableName() string { return "outbound_queue" }
