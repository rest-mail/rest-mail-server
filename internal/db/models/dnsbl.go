package models

import (
	"encoding/json"
	"time"
)

// DNSBLListingEvent is one authoritative listing or delisting. History is
// retained indefinitely; the latest ID per IP/provider/zone seeds the monitor.
// Since is the listing's first-seen time, also on the delisting that resolves it.
type DNSBLListingEvent struct {
	ID       uint64          `gorm:"primaryKey;index:idx_dnsbl_pair,priority:4,sort:desc"`
	IP       string          `gorm:"type:inet;not null;index:idx_dnsbl_pair,priority:1"`
	Provider string          `gorm:"type:text;not null;index:idx_dnsbl_pair,priority:2"`
	Zone     string          `gorm:"type:text;not null;index:idx_dnsbl_pair,priority:3"`
	Kind     string          `gorm:"size:16;not null;check:dnsbl_event_kind,kind IN ('listed','delisted')"`
	At       time.Time       `gorm:"not null"`
	Since    time.Time       `gorm:"not null"`
	Codes    json.RawMessage `gorm:"type:jsonb;not null"`
	Reason   string          `gorm:"type:text;not null"`
	Query    string          `gorm:"type:text;not null"`
}

func (DNSBLListingEvent) TableName() string { return "dnsbl_listing_events" }
