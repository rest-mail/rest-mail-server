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
	ID        uint   `gorm:"primaryKey" json:"id"`
	MailboxID uint   `gorm:"not null;index:idx_messages_mailbox_folder;index:idx_messages_mailbox_deleted" json:"mailbox_id"`
	Folder    string `gorm:"size:255;not null;default:INBOX;index:idx_messages_mailbox_folder" json:"folder"`
	// The RFC 5322 Message-ID header (a string). Careful: the column name
	// message_id doubles as GORM's conventional FK-column name for relations
	// referencing Message, so an ambiguous association elsewhere can hijack
	// this column during AutoMigrate — see the note on OutboundQueue.
	MsgID             string `gorm:"column:message_id;size:995;index" json:"message_id"`
	InReplyTo         string `gorm:"size:995" json:"in_reply_to"`
	References        string `gorm:"type:text" json:"references"`
	ThreadID          string `gorm:"size:995;index" json:"thread_id"`
	Sender            string `gorm:"size:255;not null" json:"sender"`
	SenderName        string `gorm:"size:255" json:"sender_name"`
	RecipientsTo      JSONB  `gorm:"type:jsonb;not null;default:'[]'" json:"recipients_to"`
	RecipientsCc      JSONB  `gorm:"type:jsonb;not null;default:'[]'" json:"recipients_cc"`
	Subject           string `gorm:"type:text" json:"subject"`
	BodyText          string `gorm:"type:text" json:"body_text,omitempty"`
	BodyHTML          string `gorm:"type:text" json:"body_html,omitempty"`
	Headers           JSONB  `gorm:"type:jsonb" json:"headers,omitempty"`
	RawMessage        string `gorm:"type:text" json:"-"`
	CalendarEventsRaw JSONB  `gorm:"column:calendar_events;type:jsonb" json:"calendar_events,omitempty"`
	SizeBytes         int    `gorm:"not null;default:0" json:"size_bytes"`
	// RawSize is the exact octet count of RawMessage as stored — the size a
	// protocol client receives when the raw is served verbatim. IMAP
	// RFC822.SIZE (RFC 3501) and POP3 STAT/LIST must report the transmitted
	// octet count exactly, which SizeBytes (a quota heuristic over
	// subject/body lengths) does not guarantee. Zero means "no stored raw";
	// consumers fall back to SizeBytes.
	RawSize int `gorm:"not null;default:0" json:"raw_size"`
	// ReceivedTLS records the inbound transport security of an inbound-MX
	// (port 25) delivery: true = the connection was encrypted (implicit TLS or
	// STARTTLS), false = plaintext. It is a pointer so NULL can mean "not an
	// inbound-MX delivery / unknown" (local webmail send, IMAP APPEND,
	// authenticated submission, and every row created before this column
	// existed) — kept distinct from a real plaintext arrival so the dashboard's
	// inbound-MX denominator (received_tls IS NOT NULL) and the plaintext count
	// stay correct. Always collected for inbound MX; there is no toggle.
	ReceivedTLS *bool `gorm:"index" json:"received_tls,omitempty"`
	// TLSVersion is the negotiated TLS version label (e.g. "TLS1.3") for an
	// encrypted inbound-MX delivery; empty when plaintext or not applicable.
	TLSVersion     string     `gorm:"size:16" json:"tls_version,omitempty"`
	HasAttachments bool       `gorm:"default:false" json:"has_attachments"`
	IsRead         bool       `gorm:"default:false" json:"is_read"`
	IsFlagged      bool       `gorm:"default:false" json:"is_flagged"`
	IsStarred      bool       `gorm:"default:false" json:"is_starred"`
	IsDraft        bool       `gorm:"default:false" json:"is_draft"`
	IsDeleted      bool       `gorm:"default:false;index:idx_messages_mailbox_deleted" json:"is_deleted"`
	ReceivedAt     time.Time  `gorm:"default:now();index" json:"received_at"`
	DateHeader     *time.Time `json:"date_header"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	// Associations
	Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"mailbox,omitempty"`
}

func (Message) TableName() string { return "messages" }
