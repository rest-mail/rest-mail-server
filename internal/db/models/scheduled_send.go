package models

import "time"

// ScheduledSend is a message the user wrote now and asked the server to send
// later. The request is kept whole, so the send at the due time goes through
// exactly the same handler (and the same checks) as one sent at once.
type ScheduledSend struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	AccountID uint      `gorm:"not null;index" json:"account_id"`
	From      string    `gorm:"size:255;not null" json:"from"`
	To        JSONB     `gorm:"type:jsonb;not null;default:'[]'" json:"to"`
	Subject   string    `gorm:"type:text" json:"subject"`
	SendAt    time.Time `gorm:"not null;index" json:"scheduled_at"`
	// Status values: pending, sending, sent, failed, cancelled.
	Status   string `gorm:"size:20;not null;default:pending;index" json:"status"`
	Attempts int    `gorm:"not null;default:0" json:"attempts"`
	// Request is the POST /messages/send body as it arrived, with
	// scheduled_at removed, so replaying it sends rather than schedules.
	Request       JSONB     `gorm:"type:jsonb;not null" json:"-"`
	SentMessageID *uint     `gorm:"index" json:"sent_message_id,omitempty"`
	LastError     string    `gorm:"type:text" json:"last_error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (ScheduledSend) TableName() string { return "scheduled_sends" }
