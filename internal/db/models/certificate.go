package models

import "time"

type Certificate struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	DomainID  uint      `gorm:"index" json:"domain_id"`
	CertPEM   string    `gorm:"type:text;not null" json:"-"`
	KeyPEM    string    `gorm:"type:text;not null" json:"-"` // encrypted at rest with MASTER_KEY
	Issuer    string    `gorm:"size:255" json:"issuer"`      // "letsencrypt", "self-signed", "manual"
	NotBefore time.Time `gorm:"not null" json:"not_before"`
	NotAfter  time.Time `gorm:"not null" json:"not_after"`
	AutoRenew bool      `gorm:"default:true" json:"auto_renew"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Associations
	Domain Domain `gorm:"foreignKey:DomainID" json:"domain,omitempty"`
}

func (Certificate) TableName() string { return "certificates" }
