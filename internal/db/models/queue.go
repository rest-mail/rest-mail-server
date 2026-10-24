package models

import (
	"time"

	"gorm.io/gorm"
)

type OutboundQueue struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	MessageID     *uint      `gorm:"index" json:"message_id"`
	Sender        string     `gorm:"size:255;not null" json:"sender"`
	Recipient     string     `gorm:"size:255;not null" json:"recipient"`
	Domain        string     `gorm:"size:255;not null;index" json:"domain"` // destination domain for MX lookup
	// SendingDomain is the hosted domain the message is sent as when its envelope
	// sender does not say: a vacation reply goes with a null reverse path (RFC
	// 5230 §5.1), yet is signed as, routed for and suppressed by the replying
	// mailbox's domain. Empty means the envelope sender's domain.
	SendingDomain string     `gorm:"size:255" json:"-"`
	RawMessage    string     `gorm:"type:text" json:"-"`                   // RFC 2822 formatted message
	// BodyType is the SMTP BODY= parameter declared for this message at submission
	// (7BIT, 8BITMIME, or BINARYMIME; empty when the client declared none). The
	// outbound worker consults it so it never relays 8-bit content to a next hop
	// that does not advertise 8BITMIME (RFC 6152 §3).
	BodyType      string     `gorm:"size:16" json:"body_type"`
	Status        string     `gorm:"size:20;not null;default:pending;index" json:"status"`
	// Status values: pending, delivering, deferred, delivered, bounced, expired
	Attempts      int        `gorm:"default:0" json:"attempts"`
	LastAttempt   *time.Time `json:"last_attempt"`
	NextAttempt   time.Time  `gorm:"default:now();index" json:"next_attempt"`
	LastError     string     `gorm:"type:text" json:"last_error"`
	LastErrorCode int        `json:"last_error_code"`
	// FailureCategory is what kind of failure the last attempt was, classified
	// from the remote's reply (queue.ClassifyFailure); empty until an attempt
	// fails. It decides how the message is retried, and whether at all.
	FailureCategory FailureCategory `gorm:"size:32" json:"failure_category"`
	MaxRetries    int        `gorm:"default:30" json:"max_retries"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`

	// NOTE: no Message association here, deliberately. It was never used (the
	// queue carries RawMessage inline), and under
	// DisableForeignKeyConstraintWhenMigrating GORM misresolved
	// `Message *Message gorm:"foreignKey:MessageID;references:ID"` as a has-one
	// with the FK matched BY COLUMN NAME to messages.message_id (the RFC 5322
	// Message-ID string, models.Message.MsgID) — and auto-migrated that column
	// to bigint, breaking storage of every inbound message. MessageID above is
	// a plain indexed column; resolve it manually if you need the message.
}

func (OutboundQueue) TableName() string { return "outbound_queue" }

// FailureCategory is the kind of a failed delivery attempt. A permanent one
// bounces the message at once; a transient one is retried on a schedule of its
// own (see the queue package's retry schedules).
type FailureCategory string

// Permanent failures: retrying cannot help, so the message bounces at once.
const (
	FailureBadMailbox     FailureCategory = "bad_mailbox" // no such mailbox, or it is disabled
	FailureBadDomain      FailureCategory = "bad_domain"  // no such domain, or it accepts no mail
	FailurePolicy         FailureCategory = "policy"      // refused by policy: spam, reputation, authentication
	FailureOtherPermanent FailureCategory = "other_permanent"
)

// Transient failures: the message is retried.
const (
	FailureMailboxFull    FailureCategory = "mailbox_full" // even as a 5xx: RFC 3463 makes X.2.2 transient
	FailureGreylisted     FailureCategory = "greylisted"
	FailureRateLimited    FailureCategory = "rate_limited"
	FailureConnection     FailureCategory = "connection" // no connection, or it broke off
	FailureTLS            FailureCategory = "tls"        // no secure connection where one is required
	FailureDNS            FailureCategory = "dns"        // a lookup that failed for now
	FailureOtherTransient FailureCategory = "other_transient"
)

// Permanent reports whether a failure of this category bounces the message
// rather than being retried.
func (c FailureCategory) Permanent() bool {
	switch c {
	case FailureBadMailbox, FailureBadDomain, FailurePolicy, FailureOtherPermanent:
		return true
	}
	return false
}

// Description says in a sentence what went wrong, for the sender's bounce.
func (c FailureCategory) Description() string {
	switch c {
	case FailureBadMailbox:
		return "The recipient's mailbox does not exist."
	case FailureBadDomain:
		return "The recipient's domain does not exist or accepts no mail."
	case FailurePolicy:
		return "The recipient's server refused the message by its policy, for example as spam or for failing sender authentication."
	case FailureOtherPermanent:
		return "The recipient's server refused the message."
	case FailureMailboxFull:
		return "The recipient's mailbox is full."
	case FailureGreylisted:
		return "The recipient's server kept asking for the message to be sent again later."
	case FailureRateLimited:
		return "The recipient's server is limiting how much mail it accepts from this server."
	case FailureConnection:
		return "The recipient's server could not be reached."
	case FailureTLS:
		return "A secure connection to the recipient's server could not be made."
	case FailureDNS:
		return "The recipient's domain could not be looked up."
	case FailureOtherTransient:
		return "The recipient's server could not accept the message."
	}
	return ""
}

// BeforeCreate guarantees every queued message has a delivery deadline and a
// retry budget, on ANY enqueue path. Without an ExpiresAt the worker's claim
// query (`... AND expires_at > now`) silently skips the row forever: an
// enqueue that forgot to set it (as the API /messages/send path did) left
// expires_at at the zero time and the message never delivered. Defaulting
// here — rather than at each call site — makes that class of bug impossible.
func (q *OutboundQueue) BeforeCreate(*gorm.DB) error {
	if q.ExpiresAt.IsZero() {
		q.ExpiresAt = time.Now().Add(72 * time.Hour)
	}
	if q.MaxRetries == 0 {
		q.MaxRetries = 30
	}
	return nil
}
