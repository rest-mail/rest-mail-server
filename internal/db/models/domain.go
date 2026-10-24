package models

import "time"

// Domain is a mail domain served by this installation.
//
// It is not live until it has a certificate of its own: mail for it is only ever
// carried over TLS, and the gateways refuse handshakes for a domain they cannot
// serve rather than answering under another name (issues #289, #290). Active
// therefore defaults to false — a row created without saying otherwise is not
// serving mail — and going live is a deliberate update, refused while there is no
// currently valid certificate.
type Domain struct {
	ID                uint   `gorm:"primaryKey" json:"id"`
	Name              string `gorm:"size:255;not null;uniqueIndex" json:"name"`
	ServerType        string `gorm:"size:20;not null;default:traditional" json:"server_type"` // 'traditional' or 'restmail'
	Active            bool   `gorm:"not null;default:false" json:"active"`
	DefaultQuotaBytes int64  `gorm:"default:1073741824" json:"default_quota_bytes"` // 1GB
	DKIMSelector      string `gorm:"size:63" json:"dkim_selector"`
	DKIMPrivateKey    string `gorm:"type:text" json:"-"`
	SendingPolicy     string `gorm:"size:20;not null;default:''" json:"sending_policy"` // see SendingPolicyTransactional, SendingPolicyBulk; empty: no rule (#144)
	// DMARCPolicy is the p= the domain's DMARC record asks receivers to apply to mail
	// that fails it: DMARCPolicyNone (the default), DMARCPolicyQuarantine or
	// DMARCPolicyReject (#729).
	DMARCPolicy string `gorm:"size:16;not null;default:none;check:dmarc_policy_values,dmarc_policy IN ('none','quarantine','reject')" json:"dmarc_policy"`
	// DKIMCreatedAt is when the current key was installed, not the domain age.
	// Legacy keys have no known installation time and remain NULL.
	DKIMCreatedAt *time.Time `json:"-"`
	// DKIMRotationIntervalDays is the requested key lifetime; zero disables
	// scheduled rotation. Exposed only through the DKIM settings API.
	DKIMRotationIntervalDays int       `gorm:"not null;default:0;check:dkim_rotation_interval_range,dkim_rotation_interval_days >= 0 AND dkim_rotation_interval_days <= 3650" json:"-"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`

	// Associations
	Mailboxes []Mailbox `gorm:"foreignKey:DomainID" json:"mailboxes,omitempty"`
	Aliases   []Alias   `gorm:"foreignKey:DomainID" json:"aliases,omitempty"`
}

// MaxDKIMRotationIntervalDays bounds configuration and keeps conversion to a
// time.Duration safe. Keep the domains check constraint in sync.
const MaxDKIMRotationIntervalDays = 3650

// A domain's DMARC policies (Domain.DMARCPolicy, RFC 9989 §4.7), what a receiver is asked to
// do with mail from it that fails DMARC. A new domain starts at none, which changes no one's
// delivery and brings the aggregate reports in; RFC 9989 §7.4 asks a domain whose users write
// ordinary mail to read a month of them at none, then at quarantine, before moving to reject,
// and not to publish reject at all when its users post to mailing lists (#729).
const (
	DMARCPolicyNone       = "none"
	DMARCPolicyQuarantine = "quarantine"
	DMARCPolicyReject     = "reject"
)

// A domain's sending policies (Domain.SendingPolicy, #144).
const (
	// SendingPolicyTransactional is mail one person asked for (receipts, resets,
	// notifications): it carries no List-Unsubscribe field, so the send API
	// refuses list_unsubscribe.
	SendingPolicyTransactional = "transactional"
	// SendingPolicyBulk is mail sent to a list: receivers such as Gmail and Yahoo
	// expect every message to say how to unsubscribe, so the send API refuses
	// one without list_unsubscribe.
	SendingPolicyBulk = "bulk"
)
