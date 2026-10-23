// Code generated from internal/db/models by generate_testing_snapshot.py; DO NOT EDIT.
package handlers

import (
	"encoding/json"
	"github.com/restmail/restmail/internal/db/models"
	"time"
)

type TestSnapshotDomain struct {
	ID                uint                  `json:"id"`
	Name              string                `json:"name"`
	ServerType        string                `json:"server_type"`
	Active            bool                  `json:"active"`
	DefaultQuotaBytes int64                 `json:"default_quota_bytes"`
	DKIMSelector      string                `json:"dkim_selector"`
	SendingPolicy     string                `json:"sending_policy"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
	Mailboxes         []TestSnapshotMailbox `json:"mailboxes,omitempty"`
	Aliases           []TestSnapshotAlias   `json:"aliases,omitempty"`
}

type TestSnapshotMailbox struct {
	ID                     uint                    `json:"id"`
	DomainID               uint                    `json:"domain_id"`
	LocalPart              string                  `json:"local_part"`
	Address                string                  `json:"address"`
	DisplayName            string                  `json:"display_name"`
	QuotaBytes             int64                   `json:"quota_bytes"`
	QuotaUsedBytes         int64                   `json:"quota_used_bytes"`
	Active                 bool                    `json:"active"`
	LastLoginAt            *time.Time              `json:"last_login_at"`
	PasswordChangeRequired bool                    `json:"password_change_required"`
	TwoFactorRequired      bool                    `json:"two_factor_required"`
	LastPasswordChange     *time.Time              `json:"last_password_change,omitempty"`
	CreatedAt              time.Time               `json:"created_at"`
	UpdatedAt              time.Time               `json:"updated_at"`
	Domain                 TestSnapshotDomain      `json:"domain,omitempty"`
	Messages               []TestSnapshotMessage   `json:"messages,omitempty"`
	QuotaUsage             *TestSnapshotQuotaUsage `json:"quota_usage,omitempty"`
}

type TestSnapshotAlias struct {
	ID                 uint               `json:"id"`
	DomainID           uint               `json:"domain_id"`
	SourceAddress      string             `json:"source_address"`
	DestinationAddress string             `json:"destination_address"`
	Active             bool               `json:"active"`
	CreatedAt          time.Time          `json:"created_at"`
	Domain             TestSnapshotDomain `json:"domain,omitempty"`
}

type TestSnapshotMessage struct {
	ID                uint                `json:"id"`
	MailboxID         uint                `json:"mailbox_id"`
	Folder            string              `json:"folder"`
	MsgID             string              `json:"message_id"`
	InReplyTo         string              `json:"in_reply_to"`
	References        string              `json:"references"`
	ThreadID          string              `json:"thread_id"`
	Sender            string              `json:"sender"`
	SenderName        string              `json:"sender_name"`
	RecipientsTo      json.RawMessage     `json:"recipients_to"`
	RecipientsCc      json.RawMessage     `json:"recipients_cc"`
	Subject           string              `json:"subject"`
	BodyText          string              `json:"body_text,omitempty"`
	BodyHTML          string              `json:"body_html,omitempty"`
	Headers           json.RawMessage     `json:"headers,omitempty"`
	CalendarEventsRaw json.RawMessage     `json:"calendar_events,omitempty"`
	SizeBytes         int                 `json:"size_bytes"`
	RawSize           int                 `json:"raw_size"`
	ReceivedTLS       *bool               `json:"received_tls,omitempty"`
	TLSVersion        string              `json:"tls_version,omitempty"`
	HasAttachments    bool                `json:"has_attachments"`
	IsRead            bool                `json:"is_read"`
	IsDSN             bool                `json:"is_dsn"`
	IsFlagged         bool                `json:"is_flagged"`
	IsStarred         bool                `json:"is_starred"`
	IsDraft           bool                `json:"is_draft"`
	IsDeleted         bool                `json:"is_deleted"`
	ReceivedAt        time.Time           `json:"received_at"`
	DateHeader        *time.Time          `json:"date_header"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
	Mailbox           TestSnapshotMailbox `json:"mailbox,omitempty"`
}

type TestSnapshotOutboundQueue struct {
	ID              uint                   `json:"id"`
	MessageID       *uint                  `json:"message_id"`
	Sender          string                 `json:"sender"`
	Recipient       string                 `json:"recipient"`
	Domain          string                 `json:"domain"`
	BodyType        string                 `json:"body_type"`
	Status          string                 `json:"status"`
	Attempts        int                    `json:"attempts"`
	LastAttempt     *time.Time             `json:"last_attempt"`
	NextAttempt     time.Time              `json:"next_attempt"`
	LastError       string                 `json:"last_error"`
	LastErrorCode   int                    `json:"last_error_code"`
	FailureCategory models.FailureCategory `json:"failure_category"`
	MaxRetries      int                    `json:"max_retries"`
	CreatedAt       time.Time              `json:"created_at"`
	ExpiresAt       time.Time              `json:"expires_at"`
}

type TestSnapshotPipeline struct {
	ID        uint               `json:"id"`
	DomainID  uint               `json:"domain_id"`
	Direction string             `json:"direction"`
	Filters   json.RawMessage    `json:"filters"`
	Active    bool               `json:"active"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Domain    TestSnapshotDomain `json:"domain,omitempty"`
}

type TestSnapshotBan struct {
	ID        uint       `json:"id"`
	IP        string     `json:"ip"`
	Reason    string     `json:"reason"`
	Protocol  string     `json:"protocol"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type TestSnapshotQuotaUsage struct {
	MailboxID       uint                `json:"mailbox_id"`
	SubjectBytes    int64               `json:"subject_bytes"`
	BodyBytes       int64               `json:"body_bytes"`
	AttachmentBytes int64               `json:"attachment_bytes"`
	MessageCount    int                 `json:"message_count"`
	UpdatedAt       time.Time           `json:"updated_at"`
	Mailbox         TestSnapshotMailbox `json:"mailbox,omitempty"`
}

type RestoreSnapshotDomain struct {
	ID                uint                     `json:"id,omitempty"`
	Name              string                   `json:"name,omitempty"`
	ServerType        string                   `json:"server_type,omitempty"`
	Active            bool                     `json:"active,omitempty"`
	DefaultQuotaBytes int64                    `json:"default_quota_bytes,omitempty"`
	DKIMSelector      string                   `json:"dkim_selector,omitempty"`
	SendingPolicy     string                   `json:"sending_policy,omitempty"`
	CreatedAt         time.Time                `json:"created_at,omitempty"`
	UpdatedAt         time.Time                `json:"updated_at,omitempty"`
	Mailboxes         []RestoreSnapshotMailbox `json:"mailboxes,omitempty"`
	Aliases           []RestoreSnapshotAlias   `json:"aliases,omitempty"`
}

type RestoreSnapshotMailbox struct {
	ID                     uint                       `json:"id,omitempty"`
	DomainID               uint                       `json:"domain_id,omitempty"`
	LocalPart              string                     `json:"local_part,omitempty"`
	Address                string                     `json:"address,omitempty"`
	DisplayName            string                     `json:"display_name,omitempty"`
	QuotaBytes             int64                      `json:"quota_bytes,omitempty"`
	QuotaUsedBytes         int64                      `json:"quota_used_bytes,omitempty"`
	Active                 bool                       `json:"active,omitempty"`
	LastLoginAt            *time.Time                 `json:"last_login_at,omitempty"`
	PasswordChangeRequired bool                       `json:"password_change_required,omitempty"`
	TwoFactorRequired      bool                       `json:"two_factor_required,omitempty"`
	LastPasswordChange     *time.Time                 `json:"last_password_change,omitempty"`
	CreatedAt              time.Time                  `json:"created_at,omitempty"`
	UpdatedAt              time.Time                  `json:"updated_at,omitempty"`
	Domain                 RestoreSnapshotDomain      `json:"domain,omitempty"`
	Messages               []RestoreSnapshotMessage   `json:"messages,omitempty"`
	QuotaUsage             *RestoreSnapshotQuotaUsage `json:"quota_usage,omitempty"`
}

type RestoreSnapshotAlias struct {
	ID                 uint                  `json:"id,omitempty"`
	DomainID           uint                  `json:"domain_id,omitempty"`
	SourceAddress      string                `json:"source_address,omitempty"`
	DestinationAddress string                `json:"destination_address,omitempty"`
	Active             bool                  `json:"active,omitempty"`
	CreatedAt          time.Time             `json:"created_at,omitempty"`
	Domain             RestoreSnapshotDomain `json:"domain,omitempty"`
}

type RestoreSnapshotMessage struct {
	ID                uint                   `json:"id,omitempty"`
	MailboxID         uint                   `json:"mailbox_id,omitempty"`
	Folder            string                 `json:"folder,omitempty"`
	MsgID             string                 `json:"message_id,omitempty"`
	InReplyTo         string                 `json:"in_reply_to,omitempty"`
	References        string                 `json:"references,omitempty"`
	ThreadID          string                 `json:"thread_id,omitempty"`
	Sender            string                 `json:"sender,omitempty"`
	SenderName        string                 `json:"sender_name,omitempty"`
	RecipientsTo      json.RawMessage        `json:"recipients_to,omitempty"`
	RecipientsCc      json.RawMessage        `json:"recipients_cc,omitempty"`
	Subject           string                 `json:"subject,omitempty"`
	BodyText          string                 `json:"body_text,omitempty"`
	BodyHTML          string                 `json:"body_html,omitempty"`
	Headers           json.RawMessage        `json:"headers,omitempty"`
	CalendarEventsRaw json.RawMessage        `json:"calendar_events,omitempty"`
	SizeBytes         int                    `json:"size_bytes,omitempty"`
	RawSize           int                    `json:"raw_size,omitempty"`
	ReceivedTLS       *bool                  `json:"received_tls,omitempty"`
	TLSVersion        string                 `json:"tls_version,omitempty"`
	HasAttachments    bool                   `json:"has_attachments,omitempty"`
	IsRead            bool                   `json:"is_read,omitempty"`
	IsDSN             bool                   `json:"is_dsn,omitempty"`
	IsFlagged         bool                   `json:"is_flagged,omitempty"`
	IsStarred         bool                   `json:"is_starred,omitempty"`
	IsDraft           bool                   `json:"is_draft,omitempty"`
	IsDeleted         bool                   `json:"is_deleted,omitempty"`
	ReceivedAt        time.Time              `json:"received_at,omitempty"`
	DateHeader        *time.Time             `json:"date_header,omitempty"`
	CreatedAt         time.Time              `json:"created_at,omitempty"`
	UpdatedAt         time.Time              `json:"updated_at,omitempty"`
	Mailbox           RestoreSnapshotMailbox `json:"mailbox,omitempty"`
}

type RestoreSnapshotOutboundQueue struct {
	ID              uint                   `json:"id,omitempty"`
	MessageID       *uint                  `json:"message_id,omitempty"`
	Sender          string                 `json:"sender,omitempty"`
	Recipient       string                 `json:"recipient,omitempty"`
	Domain          string                 `json:"domain,omitempty"`
	BodyType        string                 `json:"body_type,omitempty"`
	Status          string                 `json:"status,omitempty"`
	Attempts        int                    `json:"attempts,omitempty"`
	LastAttempt     *time.Time             `json:"last_attempt,omitempty"`
	NextAttempt     time.Time              `json:"next_attempt,omitempty"`
	LastError       string                 `json:"last_error,omitempty"`
	LastErrorCode   int                    `json:"last_error_code,omitempty"`
	FailureCategory models.FailureCategory `json:"failure_category,omitempty"`
	MaxRetries      int                    `json:"max_retries,omitempty"`
	CreatedAt       time.Time              `json:"created_at,omitempty"`
	ExpiresAt       time.Time              `json:"expires_at,omitempty"`
}

type RestoreSnapshotPipeline struct {
	ID        uint                  `json:"id,omitempty"`
	DomainID  uint                  `json:"domain_id,omitempty"`
	Direction string                `json:"direction,omitempty"`
	Filters   json.RawMessage       `json:"filters,omitempty"`
	Active    bool                  `json:"active,omitempty"`
	CreatedAt time.Time             `json:"created_at,omitempty"`
	UpdatedAt time.Time             `json:"updated_at,omitempty"`
	Domain    RestoreSnapshotDomain `json:"domain,omitempty"`
}

type RestoreSnapshotBan struct {
	ID        uint       `json:"id,omitempty"`
	IP        string     `json:"ip,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	Protocol  string     `json:"protocol,omitempty"`
	CreatedBy string     `json:"created_by,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at,omitempty"`
}

type RestoreSnapshotQuotaUsage struct {
	MailboxID       uint                   `json:"mailbox_id,omitempty"`
	SubjectBytes    int64                  `json:"subject_bytes,omitempty"`
	BodyBytes       int64                  `json:"body_bytes,omitempty"`
	AttachmentBytes int64                  `json:"attachment_bytes,omitempty"`
	MessageCount    int                    `json:"message_count,omitempty"`
	UpdatedAt       time.Time              `json:"updated_at,omitempty"`
	Mailbox         RestoreSnapshotMailbox `json:"mailbox,omitempty"`
}
