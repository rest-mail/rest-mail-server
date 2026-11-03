package models

import "time"

type QuietNotification struct {
	MailboxID uint      `gorm:"primaryKey;autoIncrement:false"`
	MessageID uint      `gorm:"primaryKey;autoIncrement:false"`
	ArrivedAt time.Time `gorm:"not null"`
	Start     string    `gorm:"size:5;not null"`
	End       string    `gorm:"size:5;not null"`
	TimeZone  string    `gorm:"size:255;not null"`
	Mailbox   Mailbox   `gorm:"foreignKey:MailboxID;constraint:OnDelete:CASCADE"`
}
