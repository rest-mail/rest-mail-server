package models

import "time"

// OneTimeTokenResetAdmin is the purpose of a token that lets the owner of an
// account who has lost every way in choose a new password and enroll a new
// authenticator.
const OneTimeTokenResetAdmin = "reset_admin"

// OneTimeToken is an owner recovery token or an account invitation issued by
// an authorized administrator, redeemed once over the public API. Only a
// SHA-256 of the token is stored, so reading the table grants no access.
type OneTimeToken struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Purpose string `gorm:"size:32;not null;index" json:"purpose"`
	// TokenHash is the hex SHA-256 of the token handed out.
	TokenHash string `gorm:"size:64;uniqueIndex;not null" json:"-"`
	// AccountID is the account the reset or invitation token is for.
	AccountID *uint      `json:"account_id,omitempty"`
	ExpiresAt time.Time  `gorm:"not null;index" json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func (OneTimeToken) TableName() string { return "one_time_tokens" }
