package models

import "time"

// DomainRetirement is what remains of a domain deleted through the API until it is
// restored. Its name is looked up for each recipient no live domain answers for.
type DomainRetirement struct {
	DomainID  uint   `gorm:"primaryKey;autoIncrement:false"`
	Name      string `gorm:"not null;index"`
	State     JSONB  `gorm:"type:jsonb;not null"`
	CreatedAt time.Time
}
