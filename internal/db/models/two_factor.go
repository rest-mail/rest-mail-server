package models

import "time"

// Two-factor owner types. A 2FA enrollment belongs to either a mailbox user
// (SubjectID == mailbox ID) or an admin user (SubjectID == admin_users ID),
// mirroring the refresh-token ledger's (UserType, SubjectID) ownership key so a
// single table serves both login surfaces.
const (
	TwoFactorUserTypeMailbox = "mailbox"
	TwoFactorUserTypeAdmin   = "admin"
)

// TwoFactor is one account's optional TOTP (RFC 6238) two-factor enrollment
// (OSI-19). At most one row exists per owner (unique on UserType+SubjectID).
//
// Enrollment is two-phase to prevent lockout from a mistyped/misprovisioned
// secret: a row is first created with Confirmed=false (the secret is generated
// and the otpauth URI handed to the user), and only flips to Confirmed=true
// after the user submits a first valid code. Login enforcement keys strictly on
// Confirmed — an unconfirmed row never gates login, so a half-finished
// enrollment can't lock anyone out.
//
// The TOTP secret is NEVER stored in plaintext: EncryptedSecret holds the
// base32 secret encrypted at rest with MASTER_KEY via internal/crypto
// (AES-256-GCM), the same mechanism used for DKIM/ACME private keys.
type TwoFactor struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// UserType + SubjectID identify the owning account and together form the
	// unique key (an account has at most one enrollment).
	UserType  string `gorm:"size:16;not null;uniqueIndex:idx_two_factor_owner" json:"user_type"`
	SubjectID uint   `gorm:"not null;uniqueIndex:idx_two_factor_owner" json:"subject_id"`
	// EncryptedSecret is the base32 TOTP secret encrypted at rest with
	// MASTER_KEY (base64 AES-256-GCM ciphertext). Never serialized to clients.
	EncryptedSecret string `gorm:"type:text;not null" json:"-"`
	// Confirmed marks a completed enrollment. Only a confirmed row gates login.
	Confirmed   bool       `gorm:"not null;default:false;index" json:"confirmed"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`

	// RecoveryCodes are the account's one-time recovery codes (hashed). Deleted
	// alongside the enrollment on disable / re-enroll.
	RecoveryCodes []TwoFactorRecoveryCode `gorm:"foreignKey:TwoFactorID" json:"-"`
}

func (TwoFactor) TableName() string { return "two_factor" }

// TwoFactorRecoveryCode is one single-use recovery code for a 2FA enrollment,
// stored ONLY as a bcrypt hash ({BLF-CRYPT}, the same helper passwords use).
// The plaintext is shown to the user exactly once at enrollment. A code is
// consumed by stamping UsedAt (guarded so it can be redeemed at most once).
type TwoFactorRecoveryCode struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	TwoFactorID uint       `gorm:"not null;index" json:"two_factor_id"`
	CodeHash    string     `gorm:"size:255;not null" json:"-"`
	UsedAt      *time.Time `json:"used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (TwoFactorRecoveryCode) TableName() string { return "two_factor_recovery_codes" }
