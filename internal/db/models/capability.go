package models

import "time"

// RESTMAILCapability caches per-domain RESTMAIL protocol probe results.
// Positive results (Supported=true) are cached for 1 hour; negative for 15 minutes.
type RESTMAILCapability struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Domain      string    `gorm:"size:255;uniqueIndex;not null" json:"domain"`
	Supported   bool      `gorm:"not null" json:"supported"`
	EndpointURL string    `gorm:"size:500" json:"endpoint_url"`
	LastProbed  time.Time `gorm:"not null" json:"last_probed"`
	ExpiresAt   time.Time `gorm:"not null;index" json:"expires_at"`
}

func (RESTMAILCapability) TableName() string { return "restmail_capabilities" }
