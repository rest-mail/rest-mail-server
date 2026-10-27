package models

import "time"

// Mailbox is an account: an address that receives mail, signs in with its
// credentials (Credential), and holds grants (Grant). There is no other kind
// of account: an administrator is an account holding admin grants.
type Mailbox struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	DomainID       uint       `gorm:"not null;index" json:"domain_id"`
	LocalPart      string     `gorm:"size:64;not null" json:"local_part"`
	Address        string     `gorm:"size:255;not null;uniqueIndex" json:"address"`
	DisplayName    string     `gorm:"size:255" json:"display_name"`
	QuotaBytes     int64      `gorm:"default:1073741824" json:"quota_bytes"`
	QuotaUsedBytes int64      `gorm:"default:0" json:"quota_used_bytes"`
	Active         bool       `gorm:"default:true" json:"active"`
	LastLoginAt    *time.Time `json:"last_login_at"`
	// PasswordChangeRequired keeps the account in setup until it chooses its
	// own password: set on the bootstrap account, whose password someone else
	// chose.
	PasswordChangeRequired bool `gorm:"not null;default:false" json:"password_change_required"`
	// TwoFactorRequired keeps the account in setup until it has a confirmed
	// TOTP credential, and forbids disabling 2FA afterwards. Set on the
	// bootstrap account.
	TwoFactorRequired bool `gorm:"not null;default:false" json:"two_factor_required"`
	// CredentialEpoch is stamped into every token the account is issued and
	// bumped whenever its credentials change (a reset, a new password). A token
	// carrying an older one is dead at once, whatever its expiry says.
	CredentialEpoch int `gorm:"not null;default:0" json:"-"`
	// BootstrapExpiresAt is when the deploy-time password stops working, set
	// only on the bootstrap account and cleared when it chooses its own. Until
	// then the account is reachable by anyone who can read the deployment's
	// Secret, so an install nobody finishes must not stay that way forever.
	BootstrapExpiresAt *time.Time `json:"-"`
	// OwnerID makes this an owned account (an agent's, or a sub account): the
	// account that created it. What it holds is cut down, live, to what its
	// owner holds (grants.Load; #341, Decided 12). Nil for an ordinary account.
	OwnerID            *uint      `gorm:"index" json:"-"`
	LastPasswordChange *time.Time `json:"last_password_change,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`

	// Associations
	Domain     Domain      `gorm:"foreignKey:DomainID" json:"domain,omitempty"`
	Messages   []Message   `gorm:"foreignKey:MailboxID" json:"messages,omitempty"`
	QuotaUsage *QuotaUsage `gorm:"foreignKey:MailboxID" json:"quota_usage,omitempty"`
}

// UniqueIndex on (domain_id, local_part)
func (Mailbox) TableName() string { return "mailboxes" }

// BootstrapExpired reports whether this is the bootstrap account still on its
// deploy-time password after that password's time ran out. Such an account
// cannot sign in, refresh, or use a session it already has: the only way back
// is to deploy again with a new bootstrap password, which re-arms it.
func (m *Mailbox) BootstrapExpired(now time.Time) bool {
	return m.PasswordChangeRequired && m.BootstrapExpiresAt != nil && !now.Before(*m.BootstrapExpiresAt)
}
