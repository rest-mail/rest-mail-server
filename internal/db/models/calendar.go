package models

import "time"

// CalendarEventVersion tracks the lifecycle of calendar events by UID.
// Each incoming calendar message (REQUEST, CANCEL, REPLY) creates a version
// record, enabling update detection and CANCEL cascade.
type CalendarEventVersion struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	MailboxID uint      `gorm:"not null;index:idx_cal_mailbox_uid" json:"mailbox_id"`
	UID       string    `gorm:"size:512;not null;index:idx_cal_mailbox_uid" json:"uid"`
	Sequence  int       `gorm:"not null;default:0" json:"sequence"`
	Method    string    `gorm:"size:32;not null" json:"method"`   // REQUEST, CANCEL, REPLY
	Status    string    `gorm:"size:32" json:"status"`            // CONFIRMED, CANCELLED, TENTATIVE
	Summary   string    `gorm:"type:text" json:"summary"`
	DTStart   *time.Time `json:"dtstart,omitempty"`
	DTEnd     *time.Time `json:"dtend,omitempty"`
	Organizer string    `gorm:"size:255" json:"organizer"`
	MessageID *uint     `gorm:"index" json:"message_id,omitempty"` // FK to messages.id
	CreatedAt time.Time `json:"created_at"`

	// Associations
	Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"-"`
	Message *Message `gorm:"foreignKey:MessageID" json:"-"`
}
