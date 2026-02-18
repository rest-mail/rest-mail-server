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

	Message Message `gorm:"foreignKey:MessageID;references:ID" json:"-"`
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
