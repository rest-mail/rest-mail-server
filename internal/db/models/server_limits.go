package models

import "time"

// ServerLimits is the one row of limits an administrator sets for the whole server. With no
// row, the defaults apply.
type ServerLimits struct {
	ID uint `gorm:"primaryKey"`
	// ContactImportMaxBytes is the largest contact import file the server takes.
	ContactImportMaxBytes int64 `gorm:"not null"`
	UpdatedAt             time.Time
}

func (ServerLimits) TableName() string { return "server_limits" }
