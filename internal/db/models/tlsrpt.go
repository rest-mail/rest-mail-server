package models

import (
	"encoding/json"
	"time"
)

// TLSReport stores a TLS-RPT (RFC 8460) report received from external MTAs.
// These reports contain aggregate statistics about TLS connectivity issues
// encountered when sending mail to our domains.
type TLSReport struct {
	ID              uint             `gorm:"primaryKey" json:"id"`
	DomainID        uint             `gorm:"index;not null" json:"domain_id"`
	ReportingOrg    string           `gorm:"size:255;not null" json:"reporting_org"`
	StartDate       time.Time        `gorm:"not null" json:"start_date"`
	EndDate         time.Time        `gorm:"not null" json:"end_date"`
	PolicyType      string           `gorm:"size:20;not null" json:"policy_type"` // "sts", "tlsa", "no-policy"
	PolicyDomain    string           `gorm:"size:255;not null" json:"policy_domain"`
	TotalSuccessful int64            `gorm:"default:0" json:"total_successful"`
	TotalFailure    int64            `gorm:"default:0" json:"total_failure"`
	FailureDetails  json.RawMessage  `gorm:"type:jsonb" json:"failure_details,omitempty"`
	RawReport       string           `gorm:"type:text" json:"raw_report,omitempty"`
	ReceivedAt      time.Time        `gorm:"not null" json:"received_at"`
	CreatedAt       time.Time        `json:"created_at"`
	Domain          Domain           `gorm:"foreignKey:DomainID" json:"domain,omitempty"`
}
