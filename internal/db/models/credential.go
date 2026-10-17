package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Credential kinds. App passwords come first (#344); the mailbox's own
// password, TOTP, OAuth client secrets and public keys follow (#341).
const CredentialKindAppPassword = "app_password"

// Who a credential belongs to, and who created it. Mailboxes only, for now.
const CredentialOwnerMailbox = "mailbox"

// The capabilities a credential's scope holds on its mailbox.
const (
	CredentialCapRead  = "mail:read"
	CredentialCapWrite = "mail:write"
	CredentialCapSend  = "mail:send"
)

// Credential is one way of proving to be an account: 0..n per mailbox, each
// named and revoked on its own. Revoking one keeps its row, for the audit
// trail; nothing deletes credentials.
//
// The secret is never stored: SecretHash is the hex SHA-256 of an app
// password's secret (auth.HashAppPasswordSecret), and PublicID, the part of
// the app password before the secret, is how a sign-in finds the row.
type Credential struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// OwnerKind and OwnerID are the account the credential proves: a mailbox.
	OwnerKind string `gorm:"size:16;not null;index:idx_credentials_owner" json:"owner_kind"`
	OwnerID   uint   `gorm:"not null;index:idx_credentials_owner" json:"owner_id"`
	// Kind is one of the CredentialKind* kinds.
	Kind string `gorm:"size:32;not null" json:"kind"`
	// Name is what the owner calls it: "iPhone Mail", "n8n invoices".
	Name       string `gorm:"size:100;not null" json:"name"`
	PublicID   string `gorm:"size:32;not null;uniqueIndex" json:"public_id"`
	SecretHash string `gorm:"size:64;not null" json:"-"`
	// Scope is what the credential may reach. For an app password, for now,
	// always its own mailbox with read, write and send.
	Scope CredentialScope `gorm:"type:jsonb;not null" json:"scope"`
	// CreatedByKind and CreatedByID are the account that made it.
	CreatedByKind string     `gorm:"size:16;not null" json:"created_by_kind"`
	CreatedByID   uint       `gorm:"not null" json:"created_by_id"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	ExpiresAt     *time.Time `json:"expires_at"`
	RevokedAt     *time.Time `gorm:"index" json:"revoked_at"`
}

func (Credential) TableName() string { return "credentials" }

// Usable reports whether the credential may still be used at now: not revoked,
// and not expired.
func (c *Credential) Usable(now time.Time) bool {
	return c.RevokedAt == nil && (c.ExpiresAt == nil || now.Before(*c.ExpiresAt))
}

// CredentialScope is what a credential may reach: one mailbox, and the
// capabilities it holds there.
type CredentialScope struct {
	MailboxID    uint     `json:"mailbox_id"`
	Capabilities []string `json:"capabilities"`
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
