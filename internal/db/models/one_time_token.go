package models

import "time"

// One-time token purposes.
const (
	// OneTimeTokenSetup claims a server that has no admin yet.
	OneTimeTokenSetup = "setup"
	// OneTimeTokenResetAdmin lets an admin who has lost every way in choose a
	// new password and enroll a new authenticator.
	OneTimeTokenResetAdmin = "reset_admin"
	// OneTimeTokenSetupCode claims a server too: the code a fresh install
	// prints at startup, replaced at every start until the server is claimed.
	OneTimeTokenSetupCode = "setup_code"
)

// OneTimeToken is a single-use token, handed only to the server's owner — the
// setup code a fresh install prints, `restmail-api mint-token` on the server,
// or an owner key's signed request — and redeemed once over the public API. Only a SHA-256 of the
// token is stored, so the table is no use to someone who can read it.
type OneTimeToken struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Purpose string `gorm:"size:32;not null;index" json:"purpose"`
	// TokenHash is the hex SHA-256 of the token handed out.
	TokenHash string `gorm:"size:64;uniqueIndex;not null" json:"-"`
	// AdminUserID is the admin a reset_admin token is for; nil for setup.
	AdminUserID *uint      `json:"admin_user_id,omitempty"`
	ExpiresAt   time.Time  `gorm:"not null;index" json:"expires_at"`
	UsedAt      *time.Time `json:"used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (OneTimeToken) TableName() string { return "one_time_tokens" }
