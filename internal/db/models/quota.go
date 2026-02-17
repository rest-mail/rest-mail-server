package models

import "time"

type QuotaUsage struct {
	MailboxID       uint      `gorm:"primaryKey" json:"mailbox_id"`
	SubjectBytes    int64     `gorm:"default:0" json:"subject_bytes"`
	BodyBytes       int64     `gorm:"default:0" json:"body_bytes"`
	AttachmentBytes int64     `gorm:"default:0" json:"attachment_bytes"`
	MessageCount    int       `gorm:"default:0" json:"message_count"`
	UpdatedAt       time.Time `json:"updated_at"`

	// Associations
	Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"mailbox,omitempty"`
}

func (QuotaUsage) TableName() string { return "quota_usage" }

// TotalBytes returns the computed total quota usage.
func (q *QuotaUsage) TotalBytes() int64 {
	return q.SubjectBytes + q.BodyBytes + q.AttachmentBytes
}
