package models

import "time"

// RESTMAILCapability caches per-domain RESTMAIL protocol probe results.
// Positive results (Supported=true) are cached for 1 hour; negative for 15 minutes.
//
// A cached capability is bound to the exact MX host that advertised it (MXHost,
// OSI-20). The EHLO-advertised RESTMAIL endpoint is only ever learned from, and
// only ever reused for, that specific primary MX host: if the recipient domain's
// primary MX changes (rotation, or a rogue host starting to answer on a
// shared/multi-tenant relay), the stored entry no longer matches and a fresh
// probe is forced rather than trusting a capability learned from a different
// host. Only one row is kept per domain (Domain stays unique); it is replaced on
// each probe of the current primary MX.
type RESTMAILCapability struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Domain      string    `gorm:"size:255;uniqueIndex;not null" json:"domain"`
	MXHost      string    `gorm:"size:255;not null;default:''" json:"mx_host"`
	Supported   bool      `gorm:"not null" json:"supported"`
	EndpointURL string    `gorm:"size:500" json:"endpoint_url"`
	LastProbed  time.Time `gorm:"not null" json:"last_probed"`
	ExpiresAt   time.Time `gorm:"not null;index" json:"expires_at"`
}

func (RESTMAILCapability) TableName() string { return "restmail_capabilities" }
