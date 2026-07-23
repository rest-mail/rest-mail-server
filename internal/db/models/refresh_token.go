package models

import "time"

// Refresh-token lifecycle states. A stored refresh token starts Active; a
// successful refresh rotates it to Rotated (its replacement becomes the new
// Active token) and revocation (logout / password change) moves it to Revoked.
// Only an Active row may be exchanged at /auth/refresh — presenting a Rotated
// or Revoked token is refused, which both blocks reuse of a stolen/rotated
// token and gives logout real server-side teeth.
const (
	RefreshTokenActive  = "active"
	RefreshTokenRotated = "rotated"
	RefreshTokenRevoked = "revoked"
)

// RefreshToken persists one issued refresh token by its JWT ID (jti) so the
// server can rotate it on use and revoke it out-of-band. The opaque JWT itself
// is never stored — only its jti, owner identity, state, and expiry — so this
// table is a revocation/rotation ledger, not a secret store.
//
// Rows are looked up by Jti (unique) on every refresh; ExpiresAt lets a future
// pruner drop rows past the refresh-token TTL without affecting live sessions.
type RefreshToken struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Jti is the refresh token's JWT ID claim (RegisteredClaims.ID). Unique so a
	// jti maps to exactly one ledger row.
	Jti string `gorm:"size:64;uniqueIndex;not null" json:"jti"`
	// UserType mirrors the token's user_type claim ("mailbox" or "admin") and,
	// with SubjectID, identifies the owner for bulk revocation.
	UserType string `gorm:"size:16;index:idx_refresh_owner" json:"user_type"`
	// SubjectID is the owning mailbox ID (mailbox tokens) or admin user ID
	// (admin tokens). Paired with UserType for RevokeAllForSubject.
	SubjectID uint `gorm:"index:idx_refresh_owner" json:"subject_id"`
	// Status is one of RefreshToken{Active,Rotated,Revoked}. Only Active is
	// exchangeable.
	Status string `gorm:"size:16;not null;index" json:"status"`
	// ExpiresAt is the refresh token's own expiry, copied from the JWT so expired
	// ledger rows can be pruned.
	ExpiresAt time.Time `gorm:"index" json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (RefreshToken) TableName() string { return "refresh_tokens" }
