package models

import "time"

// OwnerKey is a public recovery key for this installation. Private keys never
// reach the server. Revoked rows remain so bootstrap cannot restore them.
type OwnerKey struct {
	Fingerprint string     `gorm:"primaryKey;size:100" json:"fingerprint"`
	PublicKey   string     `gorm:"type:text;not null" json:"public_key"`
	CreatedAt   time.Time  `json:"created_at"`
	RevokedAt   *time.Time `gorm:"index" json:"revoked_at,omitempty"`
}
