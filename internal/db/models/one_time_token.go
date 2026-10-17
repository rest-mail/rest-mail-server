package models

import "time"

// OneTimeTokenResetAdmin is the purpose of a token that lets the owner of an
// account who has lost every way in choose a new password and enroll a new
// authenticator.
const OneTimeTokenResetAdmin = "reset_admin"

// OneTimeToken is a single-use token, handed only to the server's owner —
// `restmail-api mint-token` on the server, or an owner key's signed request —
// and redeemed once over the public API. Only a SHA-256 of the token is
// stored, so the table is no use to someone who can read it.
type OneTimeToken struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Purpose string `gorm:"size:32;not null;index" json:"purpose"`
	// TokenHash is the hex SHA-256 of the token handed out.
	TokenHash string `gorm:"size:64;uniqueIndex;not null" json:"-"`
	// AccountID is the account a reset_admin token is for.
	AccountID *uint      `json:"account_id,omitempty"`
	ExpiresAt time.Time  `gorm:"not null;index" json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func (OneTimeToken) TableName() string { return "one_time_tokens" }
