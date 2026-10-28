package models

import "time"

const (
	OneTimeTokenVerifyRecoveryEmail = "verify_recovery_email"
	OneTimeTokenRecoverEmail        = "recover_email"
)

// RecoveryEmail is a method registered before lockout. Pending addresses never
// authorize recovery. Replacing an address clears verification and its tokens.
type RecoveryEmail struct {
	AccountID   uint   `gorm:"primaryKey;autoIncrement:false"`
	Address     string `gorm:"size:255;not null"`
	VerifiedAt  *time.Time
	FrozenUntil *time.Time
	UpdatedAt   time.Time
}

func (RecoveryEmail) TableName() string { return "recovery_emails" }
