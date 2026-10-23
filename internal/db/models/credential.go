package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/restmail/restmail/internal/auth"
)

// Credential kinds: everything an account signs in with (#341, Decided 4).
const (
	// CredentialKindPassword is a password the account signs in with
	// interactively, and through the protocol gateways when it has no TOTP.
	// SecretHash is its {BLF-CRYPT} bcrypt hash.
	CredentialKindPassword = "password"
	// CredentialKindTOTP is an authenticator (RFC 6238). EncryptedSecret is its
	// base32 secret, encrypted with MASTER_KEY; it counts once ConfirmedAt is
	// set, after a first valid code.
	CredentialKindTOTP = "totp"
	// CredentialKindRecoveryCode is a one-time code that stands in for a TOTP
	// code. SecretHash is its bcrypt hash; using it revokes it.
	CredentialKindRecoveryCode = "recovery_code"
	// CredentialKindAppPassword is a named password for a mail client or an
	// automation (#344), never taken at the interactive sign-in. SecretHash is
	// the hex SHA-256 of its secret.
	CredentialKindAppPassword = "app_password"
	// CredentialKindAPIKey is a named key an automation calls the REST API
	// with (#341), sent as Authorization: Bearer and never taken at the
	// interactive sign-in or by the protocol gateways. SecretHash is the hex
	// SHA-256 of its secret; Scope.Permissions is what it may do, within its
	// account's live grants.
	CredentialKindAPIKey = auth.CredentialKindAPIKey
)

// The capabilities an app password's scope holds on its account's mailbox.
const (
	CredentialCapRead  = CapMailRead
	CredentialCapWrite = CapMailWrite
	CredentialCapSend  = CapMailSend
)

// Credential is one way of proving to be an account: 0..n per account, each
// revoked on its own. Revoking one keeps its row, for the audit trail; nothing
// deletes credentials.
//
// No secret is stored in the clear: SecretHash is a hash (bcrypt for
// passwords and recovery codes, SHA-256 for app passwords), and a TOTP secret
// is encrypted. PublicID names the credential without revealing it; for an app
// password it is the part before the secret, and how a sign-in finds the row.
type Credential struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// AccountID is the account (mailbox) the credential proves.
	AccountID uint `gorm:"not null;index" json:"account_id"`
	// Kind is one of the CredentialKind* kinds.
	Kind string `gorm:"size:32;not null;index" json:"kind"`
	// Name is what the owner calls it: "iPhone Mail", "n8n invoices".
	Name            string `gorm:"size:100;not null" json:"name"`
	PublicID        string `gorm:"size:32;not null;uniqueIndex" json:"public_id"`
	SecretHash      string `gorm:"type:text;not null;default:''" json:"-"`
	EncryptedSecret string `gorm:"type:text;not null;default:''" json:"-"`
	// Scope is what an app password may reach: for now, always its own
	// account's mailbox with read, write and send. The interactive kinds prove
	// the account itself, and their scope names only it.
	Scope CredentialScope `gorm:"type:jsonb;not null" json:"scope"`
	// CreatedByID is the account that made it; nil for the system (the
	// bootstrap account's password).
	CreatedByID *uint      `json:"created_by_id"`
	CreatedAt   time.Time  `json:"created_at"`
	ConfirmedAt *time.Time `json:"confirmed_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RevokedAt   *time.Time `gorm:"index" json:"revoked_at"`
}

func (Credential) TableName() string { return "credentials" }

// Usable reports whether the credential may still be used at now: not revoked,
// and not expired.
func (c *Credential) Usable(now time.Time) bool {
	return c.RevokedAt == nil && (c.ExpiresAt == nil || now.Before(*c.ExpiresAt))
}

// CredentialScope is what a credential may reach: one mailbox, and the
// capabilities it holds there; or, for an API key, its permissions.
type CredentialScope struct {
	MailboxID    uint     `json:"mailbox_id"`
	Capabilities []string `json:"capabilities"`
	// Permissions are an API key's: each capability it may use, at a scope.
	// Empty for every other kind.
	Permissions []auth.ScopedPermission `json:"permissions,omitempty"`
}

func (s CredentialScope) Value() (driver.Value, error) {
	if s.Capabilities == nil {
		s.Capabilities = []string{}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (s *CredentialScope) Scan(value interface{}) error {
	var b []byte
	switch v := value.(type) {
	case nil:
		*s = CredentialScope{}
		return nil
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("unsupported type: %T", value)
	}
	return json.Unmarshal(b, s)
}
