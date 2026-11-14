package models

import "time"

type SendIdempotency struct {
	AccountID        uint      `gorm:"primaryKey;autoIncrement:false"`
	KeyHash          string    `gorm:"primaryKey;size:64"`
	RequestHash      string    `gorm:"size:64;not null"`
	Status           int       `gorm:"not null"`
	Response         []byte    `gorm:"type:bytea;not null"`
	ExpiresAt        time.Time `gorm:"not null;index"`
	From             string    `gorm:"not null"`
	OnBehalf         bool
	SourceMailboxID  uint
	SourceCapability string
}
