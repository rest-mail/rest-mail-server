package models

import "time"

// ACMEAccount is the API's account key at one ACME CA. Renewals reuse it, so
// the server keeps one CA account per directory rather than one per order.
type ACMEAccount struct {
	Directory string    `gorm:"primaryKey;size:2048" json:"directory"`
	KeyPEM    string    `gorm:"type:text;not null" json:"-"` // encrypted at rest with MASTER_KEY
	CreatedAt time.Time `json:"created_at"`
}

func (ACMEAccount) TableName() string { return "acme_accounts" }
