package models

import "time"

// GrantActivation switches an eligible grant on for a limited time (#346): its
// holder escalates, with a fresh second factor and a stated reason, and the
// grant is in force from ActivatedAt until ExpiresAt, or until EndedAt if it
// is switched off sooner.
//
// EndedAt is nil while the activation is live, and also for one that has
// expired but not yet been noticed; once noticed it is set to ExpiresAt. A
// grant has at most one activation with no EndedAt. Removing the grant removes
// its activations (grants.Remove); the audit log keeps their history.
type GrantActivation struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	GrantID     uint       `gorm:"not null;index;uniqueIndex:idx_grant_activations_live,where:ended_at IS NULL" json:"grant_id"`
	ActivatedAt time.Time  `gorm:"not null" json:"activated_at"`
	ExpiresAt   time.Time  `gorm:"not null;index" json:"expires_at"`
	Reason      string     `gorm:"type:text;not null" json:"reason"`
	EndedAt     *time.Time `json:"ended_at"`
}

func (GrantActivation) TableName() string { return "grant_activations" }
