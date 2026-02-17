package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// JSONB is a custom type for PostgreSQL JSONB columns.
type JSONB json.RawMessage

func (j JSONB) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "[]", nil
	}
	return string(j), nil
}

func (j *JSONB) Scan(value interface{}) error {
	if value == nil {
		*j = JSONB("[]")
		return nil
	}
	switch v := value.(type) {
	case []byte:
		*j = JSONB(v)
	case string:
		*j = JSONB(v)
	default:
		return fmt.Errorf("unsupported type: %T", value)
	}
	return nil
}

func (j JSONB) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("[]"), nil
	}
	return []byte(j), nil
}

func (j *JSONB) UnmarshalJSON(data []byte) error {
	*j = JSONB(data)
	return nil
}

type Message struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	MailboxID      uint      `gorm:"not null;index:idx_messages_mailbox_folder" json:"mailbox_id"`
	Folder         string    `gorm:"size:255;not null;default:INBOX;index:idx_messages_mailbox_folder" json:"folder"`
	MessageID      string    `gorm:"size:995;index" json:"message_id"`
	InReplyTo      string    `gorm:"size:995" json:"in_reply_to"`
	References     string    `gorm:"type:text" json:"references"`
	ThreadID       string    `gorm:"size:995;index" json:"thread_id"`
	Sender         string    `gorm:"size:255;not null" json:"sender"`
	SenderName     string    `gorm:"size:255" json:"sender_name"`
	RecipientsTo   JSONB     `gorm:"type:jsonb;not null;default:'[]'" json:"recipients_to"`
	RecipientsCc   JSONB     `gorm:"type:jsonb;not null;default:'[]'" json:"recipients_cc"`
	Subject        string    `gorm:"type:text" json:"subject"`
	BodyText       string    `gorm:"type:text" json:"body_text,omitempty"`
	BodyHTML       string    `gorm:"type:text" json:"body_html,omitempty"`
	Headers        JSONB     `gorm:"type:jsonb" json:"headers,omitempty"`
	RawMessage     string    `gorm:"type:text" json:"-"`
	SizeBytes      int       `gorm:"not null;default:0" json:"size_bytes"`
	HasAttachments bool      `gorm:"default:false" json:"has_attachments"`
	IsRead         bool      `gorm:"default:false" json:"is_read"`
	IsFlagged      bool      `gorm:"default:false" json:"is_flagged"`
	IsStarred      bool      `gorm:"default:false" json:"is_starred"`
	IsDraft        bool      `gorm:"default:false" json:"is_draft"`
	IsDeleted      bool      `gorm:"default:false" json:"is_deleted"`
	ReceivedAt     time.Time `gorm:"default:now()" json:"received_at"`
	DateHeader     *time.Time `json:"date_header"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	// Associations
	Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"mailbox,omitempty"`
}

func (Message) TableName() string { return "messages" }
