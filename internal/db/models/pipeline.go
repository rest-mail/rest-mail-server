package models

import (
	"encoding/json"
	"time"
)

// Pipeline represents a filter pipeline configured for a domain.
type Pipeline struct {
	ID        uint            `gorm:"primaryKey" json:"id"`
	DomainID  uint            `gorm:"not null;index" json:"domain_id"`
	Direction string          `gorm:"size:20;not null" json:"direction"` // "inbound" or "outbound"
	Filters   json.RawMessage `gorm:"type:jsonb" json:"filters"`
	Active    bool            `gorm:"default:true" json:"active"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`

	Domain Domain `gorm:"foreignKey:DomainID" json:"domain,omitempty"`
}

func (Pipeline) TableName() string { return "pipelines" }

// CustomFilter is a user-defined filter stored in the database.
type CustomFilter struct {
	ID          uint            `gorm:"primaryKey" json:"id"`
	DomainID    uint            `gorm:"not null;index" json:"domain_id"`
	Name        string          `gorm:"size:100;not null" json:"name"`
	Description string          `gorm:"size:500" json:"description"`
	FilterType  string          `gorm:"size:20;not null" json:"filter_type"` // "action" or "transform"
	Direction   string          `gorm:"size:20;not null" json:"direction"`   // "inbound", "outbound", or "both"
	Config      json.RawMessage `gorm:"type:jsonb;not null" json:"config"`
	Enabled     bool            `gorm:"default:true" json:"enabled"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`

	Domain Domain `gorm:"foreignKey:DomainID" json:"domain,omitempty"`
}

func (CustomFilter) TableName() string { return "custom_filters" }

// PipelineLog records the execution of a pipeline on a message.
type PipelineLog struct {
	ID         uint            `gorm:"primaryKey" json:"id"`
	PipelineID uint            `gorm:"index" json:"pipeline_id"`
	MessageID  *uint           `gorm:"index" json:"message_id"`
	Direction  string          `gorm:"size:20" json:"direction"`
	Action     string          `gorm:"size:20" json:"action"` // "continue", "reject", "quarantine", "discard"
	Steps      json.RawMessage `gorm:"type:jsonb" json:"steps"`
	DurationMS int64           `json:"duration_ms"`
	CreatedAt  time.Time       `json:"created_at"`
}

func (PipelineLog) TableName() string { return "pipeline_logs" }

// MessageTrace is the durable per-message observability record: exactly one row
// per message that traversed a pipeline, recording what happened to THIS
// message. It supersedes PipelineLog as the write target (PR3); PipelineLog is
// retained only for the legacy read handler until PR5 repoints it here. A new
// table (message_traces) rather than an in-place rename because the schema is a
// near-total superset with renamed columns and the old rows carry no true trace
// (no backfill — their real forensic detail is unknown).
//
// Trace-only raw PII (mail_from, rcpt_to, client_ip) is stored raw by resolved
// decision: full forensics while a row is hot, pruned at the retention horizon
// (PR4). These columns must NEVER be promoted to a metric label.
type MessageTrace struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// MessageID links to the delivered messages row. Set ONLY on the delivered
	// path (non-nil after the Message is created); nil for every non-delivered
	// outcome (rejected/quarantined/discarded/deferred), where correlation is via
	// RFCMessageID instead. Not a GORM association — the column is an indexed
	// nullable FK the reader joins explicitly.
	MessageID *uint `gorm:"index" json:"message_id"`

	// RFCMessageID is the RFC 5322 Message-ID header — the stable correlation key
	// for mail that never became a Message row (rejected/quarantined/discarded).
	RFCMessageID string `gorm:"size:255;index" json:"rfc_message_id"`

	Direction string `gorm:"size:20" json:"direction"` // inbound | outbound
	Transport string `gorm:"size:20" json:"transport"` // tls | plaintext | "" (unknown / not applicable)

	// Trace-only raw PII — see type doc. Never a metric label.
	MailFrom string `gorm:"size:255" json:"mail_from"`
	RcptTo   string `gorm:"size:255" json:"rcpt_to"` // first recipient
	ClientIP string `gorm:"size:64" json:"client_ip"`

	PipelineID  uint   `gorm:"index" json:"pipeline_id"`
	FinalAction string `gorm:"size:20" json:"final_action"`

	// Outcome is the bounded terminal disposition; the leading column of the
	// (outcome, created_at) composite index that PR5 analytics scans.
	Outcome string `gorm:"size:20;index:idx_message_traces_outcome_created,priority:1" json:"outcome"` // delivered|queued|rejected|quarantined|discarded|deferred

	// ReasonCode is the bounded WHY of a non-continue terminal, derived once via
	// pipeline.ReasonForStep. Empty for a delivered/queued (continue) outcome.
	ReasonCode string `gorm:"size:32;index" json:"reason_code"`

	SpamScore  *float32        `json:"spam_score,omitempty"`
	DurationMS int64           `json:"duration_ms"`
	Stages     json.RawMessage `gorm:"type:jsonb" json:"stages"` // []pipeline.StepResult

	// Sampled records whether this row survived the sampling gate. PR3 captures
	// all (always true); the sampling logic that can set it false is PR4.
	Sampled bool `gorm:"default:true" json:"sampled"`

	// CreatedAt drives PR4 pruning (standalone index) and is the second column of
	// the (outcome, created_at) analytics composite.
	CreatedAt time.Time `gorm:"index;index:idx_message_traces_outcome_created,priority:2" json:"created_at"`

	// ExpiresAt is the retention horizon. The column exists now (indexed for the
	// PR4 pruner); PR3 leaves it NULL — no horizon is computed until PR4.
	ExpiresAt *time.Time `gorm:"index" json:"expires_at,omitempty"`
}

func (MessageTrace) TableName() string { return "message_traces" }

// Contact represents a known sender in a recipient's contact list.
type Contact struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	MailboxID  uint      `gorm:"not null;index" json:"mailbox_id"`
	Email      string    `gorm:"size:255;not null" json:"email"`
	Name       string    `gorm:"size:255" json:"name"`
	TrustLevel string    `gorm:"size:20;default:auto" json:"trust_level"` // "auto", "trusted", "blocked"
	Source     string    `gorm:"size:20;default:sent" json:"source"`      // "sent", "manual", "import"
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`

	Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"-"`
}

func (Contact) TableName() string { return "contacts" }

// DomainSenderRule represents an admin-managed allow/block list entry for a domain.
type DomainSenderRule struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	DomainID  uint      `gorm:"not null;index" json:"domain_id"`
	Pattern   string    `gorm:"size:255;not null" json:"pattern"`   // "spam@evil.com" or "@evil.com"
	ListType  string    `gorm:"size:10;not null" json:"list_type"`  // "allow" or "block"
	Reason    string    `gorm:"type:text" json:"reason,omitempty"`
	CreatedBy *uint     `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	Domain Domain `gorm:"foreignKey:DomainID" json:"-"`
}

func (DomainSenderRule) TableName() string { return "domain_sender_rules" }

// GreylistEntry tracks sender/recipient/IP triples for greylisting.
type GreylistEntry struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	Sender     string    `gorm:"size:255;not null" json:"sender"`
	Recipient  string    `gorm:"size:255;not null" json:"recipient"`
	SourceIP   string    `gorm:"size:45;not null" json:"source_ip"`
	FirstSeen  time.Time `json:"first_seen"`
	RetryAfter time.Time `json:"retry_after"`
	Passed     bool      `gorm:"default:false" json:"passed"`
	CreatedAt  time.Time `json:"created_at"`
}

func (GreylistEntry) TableName() string { return "greylist_entries" }

// Quarantine holds messages that were quarantined by the pipeline.
type Quarantine struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	MailboxID        uint      `gorm:"not null;index" json:"mailbox_id"`
	Sender           string    `gorm:"size:255;not null" json:"sender"`
	Subject          string    `gorm:"type:text" json:"subject"`
	BodyPreview      string    `gorm:"type:text" json:"body_preview"`
	RawMessage       string    `gorm:"type:text;not null" json:"-"`
	SpamScore        *float32  `json:"spam_score,omitempty"`
	QuarantineReason string    `gorm:"size:50;not null" json:"quarantine_reason"`
	ReceivedAt       time.Time `json:"received_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	Released         bool      `gorm:"default:false" json:"released"`

	Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"-"`
}

func (Quarantine) TableName() string { return "quarantine" }

// VacationResponse tracks sent auto-replies to prevent duplicates.
type VacationResponse struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	MailboxID   uint      `gorm:"not null;index" json:"mailbox_id"`
	Sender      string    `gorm:"size:255;not null" json:"sender"`
	RespondedAt time.Time `json:"responded_at"`

	Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"-"`
}

func (VacationResponse) TableName() string { return "vacation_responses" }

// VacationConfig stores out-of-office auto-reply settings for a mailbox.
type VacationConfig struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	MailboxID uint       `gorm:"uniqueIndex;not null" json:"mailbox_id"`
	Enabled   bool       `gorm:"default:false" json:"enabled"`
	Subject   string     `gorm:"size:500" json:"subject"`
	Body      string     `gorm:"type:text" json:"body"`
	StartDate *time.Time `json:"start_date,omitempty"`
	EndDate   *time.Time `json:"end_date,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`

	Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"-"`
}

func (VacationConfig) TableName() string { return "vacation_configs" }

// Attachment represents a stored attachment reference.
type Attachment struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	MessageID   uint      `gorm:"not null;index" json:"message_id"`
	Filename    string    `gorm:"size:255" json:"filename"`
	ContentType string    `gorm:"size:100" json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	StorageType string    `gorm:"size:20;default:filesystem" json:"storage_type"` // "filesystem" or "s3"
	StorageRef  string    `gorm:"size:500;not null" json:"storage_ref"`
	Checksum    string    `gorm:"size:64;index" json:"checksum"` // SHA-256 hex
	CreatedAt   time.Time `json:"created_at"`

	// NOTE: no Message association here, deliberately — it was unused (queries
	// join messages by raw SQL) and, like the one removed from OutboundQueue,
	// GORM misresolved `foreignKey:MessageID` onto messages.message_id (the
	// RFC 5322 Message-ID string) during AutoMigrate, rewriting it to bigint.
}

func (Attachment) TableName() string { return "attachments" }

// SieveScript stores per-mailbox Sieve filter scripts.
type SieveScript struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	MailboxID uint      `gorm:"not null;uniqueIndex" json:"mailbox_id"`
	Script    string    `gorm:"type:text;not null" json:"script"`
	Active    bool      `gorm:"default:true" json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"-"`
}

func (SieveScript) TableName() string { return "sieve_scripts" }
