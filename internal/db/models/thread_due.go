package models

import "time"

type ThreadDue struct {
	MailboxID uint   `gorm:"primaryKey;autoIncrement:false"`
	ThreadID  string `gorm:"primaryKey;size:995"`
	Due       *time.Time
	DueSource *string `gorm:"size:16;check:thread_due_state,(due IS NULL) = (due_source IS NULL) AND (due_source IS NULL OR due_source IN ('user','rule','parsed'))"`
}
