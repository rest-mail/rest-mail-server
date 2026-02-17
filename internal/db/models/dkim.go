package models

import "time"

type DKIMKey struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	DomainID      uint      `gorm:"index;uniqueIndex:idx_domain_selector" json:"domain_id"`
	Selector      string    `gorm:"size:63;not null;uniqueIndex:idx_domain_selector" json:"selector"`
	PrivateKeyPEM string    `gorm:"type:text;not null" json:"-"` // encrypted at rest with MASTER_KEY
	PublicKeyPEM  string    `gorm:"type:text;not null" json:"public_key_pem"`
	Algorithm     string    `gorm:"size:20;default:rsa-sha256" json:"algorithm"`
	KeySize       int       `gorm:"default:2048" json:"key_size"`
	Active        bool      `gorm:"default:true" json:"active"`
	CreatedAt     time.Time `json:"created_at"`

	// Associations
	Domain Domain `gorm:"foreignKey:DomainID" json:"domain,omitempty"`
}

func (DKIMKey) TableName() string { return "dkim_keys" }
