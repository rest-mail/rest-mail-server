package models

import "time"

// Suppression is a recipient address one of our domains no longer sends to,
// because its mail to that address bounced as undeliverable: the mailbox, or
// its domain, does not exist (#141). The outbound worker bounces a message to a
// suppressed address at once, with a DSN that says so, instead of trying it,
// so a dead address isn't hammered and the domain's reputation isn't hurt.
//
// It is kept per sending domain, so one domain's admin never sees the
// addresses another domain's users write to. A row stops suppressing at
// ExpiresAt, when the address may work again; an expired row is ignored, and
// refreshed if the address bounces again.
type Suppression struct {
	ID       uint `gorm:"primaryKey" json:"id"`
	DomainID uint `gorm:"not null;uniqueIndex:idx_suppressions_domain_address" json:"domain_id"`
	// Address is the recipient, lower-cased.
	Address string            `gorm:"size:255;not null;uniqueIndex:idx_suppressions_domain_address" json:"address"`
	Reason  SuppressionReason `gorm:"size:32;not null" json:"reason"`
	// FailureCategory and LastError are the bounce that suppressed the
	// address: bad_mailbox or bad_domain, and the remote's reply.
	FailureCategory FailureCategory `gorm:"size:32;not null" json:"failure_category"`
	LastError       string          `gorm:"type:text" json:"last_error"`
	// MessageID is the sent message whose bounce suppressed the address, when
	// the queue row was linked to one.
	MessageID *uint     `json:"message_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `gorm:"not null;index" json:"expires_at"`
}

// SuppressionReason is why an address is suppressed.
type SuppressionReason string

// SuppressionHardBounce: mail to the address bounced because the mailbox, or
// its domain, does not exist.
const SuppressionHardBounce SuppressionReason = "hard_bounce"

// Suppresses reports whether a bounce of this category means the recipient
// does not exist, so that it is suppressed. A policy refusal, a full mailbox or
// a temporary failure says nothing about whether the address is alive.
func (c FailureCategory) Suppresses() bool {
	return c == FailureBadMailbox || c == FailureBadDomain
}
