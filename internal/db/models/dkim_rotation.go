package models

import (
	"errors"
	"time"
)

const (
	DKIMRotationStaged   = "staged"
	DKIMRotationPrevious = "previous"
)

// DKIMRotationKey holds a non-current key across process restarts. The current
// signing key remains on Domain; storing a row here never enables signing.
// There is at most one staged and one previous key per domain, and their
// selectors must differ. Deleting a domain removes its rotation keys.
//
// PrivateKey uses EncryptDKIMPrivateKey / LoadDKIMPrivateKey, exactly as the
// current key does. Call SetPrivateKey before inserting plaintext material.
// Never log this model or expose it as an API request type.
type DKIMRotationKey struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	DomainID   uint   `gorm:"not null;uniqueIndex:idx_dkim_rotation_domain_slot;uniqueIndex:idx_dkim_rotation_domain_selector" json:"domain_id"`
	Slot       string `gorm:"size:8;not null;uniqueIndex:idx_dkim_rotation_domain_slot;check:dkim_rotation_slot,slot IN ('staged','previous')" json:"slot"`
	Selector   string `gorm:"size:63;not null;uniqueIndex:idx_dkim_rotation_domain_selector,expression:lower(selector);check:dkim_rotation_selector,selector <> ''" json:"selector"`
	PrivateKey string `gorm:"type:text;not null;check:dkim_rotation_private_key,private_key <> ''" json:"-"`
	// InstalledAt preserves the previous key's Domain.DKIMCreatedAt, including
	// NULL for legacy keys; a staged key has not been installed yet.
	InstalledAt *time.Time `json:"installed_at"`
	// PublishedAt is recorded only after a successful DNS provider operation.
	// NULL means publication has not been recorded, not that it propagated.
	PublishedAt *time.Time `json:"published_at"`
	// TTLSeconds is the DNS TTL to wait after publication or a signing switch.
	// RFC 2181 bounds a TTL to 31 bits. Require a positive value here so
	// rotation always has an explicit overlap period.
	TTLSeconds  int64      `gorm:"not null;check:dkim_rotation_ttl,ttl_seconds > 0 AND ttl_seconds <= 2147483647" json:"ttl_seconds"`
	RetireAfter *time.Time `gorm:"check:dkim_rotation_retirement,(slot <> 'staged' OR retire_after IS NULL) AND (slot <> 'previous' OR retire_after IS NOT NULL)" json:"retire_after"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Domain      Domain     `gorm:"constraint:OnDelete:CASCADE" json:"-"`
}

// SetPrivateKey encrypts keyPEM using the same at-rest policy as the current
// signing key, including the existing plaintext development mode. A failure
// leaves the stored value unchanged. Key signability is checked by the caller.
func (k *DKIMRotationKey) SetPrivateKey(keyPEM, masterKey string) error {
	if keyPEM == "" {
		return errors.New("dkim: no rotation private key provided")
	}
	stored, err := EncryptDKIMPrivateKey(keyPEM, masterKey)
	if err != nil {
		return err
	}
	k.PrivateKey = stored
	return nil
}
