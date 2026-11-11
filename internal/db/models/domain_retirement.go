package models

import "time"

type DomainRetirement struct {
	DomainID  uint   `gorm:"primaryKey;autoIncrement:false"`
	Name      string `gorm:"not null"`
	State     JSONB  `gorm:"type:jsonb;not null"`
	CreatedAt time.Time
}
