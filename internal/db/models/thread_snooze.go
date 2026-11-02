package models

import "time"

type ThreadSnooze struct {
	MailboxID    uint       `gorm:"primaryKey;autoIncrement:false"`
	ThreadID     string     `gorm:"primaryKey;size:995"`
	SnoozedUntil *time.Time `gorm:"index"`
}
