package models

import "time"

// MTASTSPolicy stores an MTA-STS (RFC 8461) policy for a domain.
// The policy is served at /.well-known/mta-sts.txt based on the Host header.
type MTASTSPolicy struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	DomainID  uint      `gorm:"uniqueIndex;not null" json:"domain_id"`
	Mode      string    `gorm:"size:20;not null;default:'testing'" json:"mode"` // none, testing, enforce
	MXHosts   string    `gorm:"type:text" json:"mx_hosts"`                     // comma-separated
	MaxAge    int       `gorm:"not null;default:604800" json:"max_age"`        // seconds
	Active    bool      `gorm:"default:true" json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Domain    Domain    `gorm:"foreignKey:DomainID" json:"domain,omitempty"`
}
