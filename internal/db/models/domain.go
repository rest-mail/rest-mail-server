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
	ID                uint      `gorm:"primaryKey" json:"id"`
	Name              string    `gorm:"size:255;not null;uniqueIndex" json:"name"`
	ServerType        string    `gorm:"size:20;not null;default:traditional" json:"server_type"` // 'traditional' or 'restmail'
	Active            bool      `gorm:"not null;default:false" json:"active"`
	DefaultQuotaBytes int64     `gorm:"default:1073741824" json:"default_quota_bytes"` // 1GB
	DKIMSelector      string    `gorm:"size:63" json:"dkim_selector"`
	DKIMPrivateKey    string    `gorm:"type:text" json:"-"`
	SendingPolicy     string    `gorm:"size:20;not null;default:''" json:"sending_policy"` // see SendingPolicyTransactional, SendingPolicyBulk; empty: no rule (#144)
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`

	// Associations
	Mailboxes []Mailbox `gorm:"foreignKey:DomainID" json:"mailboxes,omitempty"`
	Aliases   []Alias   `gorm:"foreignKey:DomainID" json:"aliases,omitempty"`
}

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
