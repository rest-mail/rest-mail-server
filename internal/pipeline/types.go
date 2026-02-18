package pipeline

import (
	"encoding/json"
	"time"
)

// EmailJSON is the canonical JSON representation of an email as it flows
// through the pipeline. It mirrors the MIME structure: nested multipart bodies,
// structured headers, and separated attachments.
type EmailJSON struct {
	Envelope       Envelope          `json:"envelope"`
	Headers        Headers           `json:"headers"`
	Body           Body              `json:"body"`
	Attachments    []Attachment      `json:"attachments,omitempty"`
	Inline         []Attachment      `json:"inline,omitempty"`
	CalendarEvents []CalendarEvent   `json:"calendar_events,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// CalendarEvent represents a parsed VEVENT from an iCalendar (text/calendar) MIME part.
type CalendarEvent struct {
	Method      string            `json:"method"`                 // REQUEST, REPLY, CANCEL, etc.
	UID         string            `json:"uid"`                    // Unique event identifier
	Summary     string            `json:"summary"`                // Event title
	Description string            `json:"description,omitempty"`  // Event description
	Location    string            `json:"location,omitempty"`     // Event location
	DTStart     time.Time         `json:"dtstart"`                // Start time
	DTEnd       time.Time         `json:"dtend"`                  // End time
	AllDay      bool              `json:"all_day"`                // True if this is an all-day event
	Organizer   CalendarAddress   `json:"organizer"`              // Event organizer
	Attendees   []CalendarAddress `json:"attendees,omitempty"`    // Event attendees
	Status      string            `json:"status,omitempty"`       // CONFIRMED, TENTATIVE, CANCELLED
	Sequence    int               `json:"sequence"`               // Update sequence number
	DTStamp     time.Time         `json:"dtstamp,omitempty"`      // Timestamp of the calendar object
}

// CalendarAddress represents an organizer or attendee with optional metadata.
type CalendarAddress struct {
	Address  string `json:"address"`            // Email address (stripped of mailto:)
	Name     string `json:"name,omitempty"`     // Display name (CN= parameter)
	Role     string `json:"role,omitempty"`     // REQ-PARTICIPANT, OPT-PARTICIPANT, etc.
	PartStat string `json:"partstat,omitempty"` // NEEDS-ACTION, ACCEPTED, DECLINED, TENTATIVE
	RSVP     bool   `json:"rsvp,omitempty"`     // Whether RSVP is requested
}

// Envelope holds the SMTP envelope information.
type Envelope struct {
	MailFrom       string   `json:"mail_from"`
	RcptTo         []string `json:"rcpt_to"`
	ClientIP       string   `json:"client_ip,omitempty"`
	ClientHostname string   `json:"client_hostname,omitempty"`
	Helo           string   `json:"helo,omitempty"`
	TLS            bool     `json:"tls"`
	Direction      string   `json:"direction"` // "inbound" or "outbound"
}

// Address represents a structured email address.
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// Headers holds parsed and raw headers.
type Headers struct {
	From       []Address              `json:"from"`
	To         []Address              `json:"to"`
	Cc         []Address              `json:"cc,omitempty"`
	Bcc        []Address              `json:"bcc,omitempty"`
	Subject    string                 `json:"subject"`
	Date       string                 `json:"date"`
	MessageID  string                 `json:"message-id"`
	InReplyTo  string                 `json:"in-reply-to,omitempty"`
	References []string               `json:"references,omitempty"`
	Raw        map[string][]string    `json:"raw,omitempty"`
	Extra      map[string]string      `json:"extra,omitempty"`
}

// Body represents the email body, potentially with multipart parts.
type Body struct {
	ContentType string `json:"content_type"`
	Content     string `json:"content,omitempty"`
	Parts       []Body `json:"parts,omitempty"`
}

// Attachment represents an email attachment or inline image.
type Attachment struct {
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Disposition string `json:"disposition,omitempty"` // "attachment" or "inline"
	ContentID   string `json:"content_id,omitempty"`  // for inline images
	Content     string `json:"content,omitempty"`     // base64 encoded, before extraction
	Storage     string `json:"storage,omitempty"`     // "filesystem" or "s3", after extraction
	Ref         string `json:"ref,omitempty"`         // storage path, after extraction
	Checksum    string `json:"checksum,omitempty"`    // SHA-256 hex
}

// ── Filter Result Types ─────────────────────────────────────────────

// FilterType distinguishes action filters from transform filters.
type FilterType string

const (
	FilterTypeAction    FilterType = "action"
	FilterTypeTransform FilterType = "transform"
)

// Action represents the decision an action filter makes.
type Action string

const (
	ActionContinue   Action = "continue"
	ActionReject     Action = "reject"
	ActionQuarantine Action = "quarantine"
	ActionDiscard    Action = "discard"
	ActionDefer      Action = "defer"
)

// FilterResult is the output of executing a filter on an email.
type FilterResult struct {
	Type         FilterType        `json:"type"`
	Action       Action            `json:"action"`
	Message      *EmailJSON        `json:"message,omitempty"`      // non-nil for transform filters
	SkipFilters  []string          `json:"skip_filters,omitempty"` // filters to skip downstream
	RejectMsg    string            `json:"reject_message,omitempty"`
	Log          FilterLog         `json:"log"`
}

// FilterLog is the structured log output of a filter execution.
type FilterLog struct {
	Filter   string `json:"filter"`
	Result   string `json:"result"`
	Detail   string `json:"detail,omitempty"`
	Duration time.Duration `json:"duration_ms,omitempty"`
}

// ── Pipeline Configuration ──────────────────────────────────────────

// PipelineConfig describes a pipeline as stored in the database.
type PipelineConfig struct {
	ID        uint              `json:"id"`
	DomainID  uint              `json:"domain_id"`
	Direction string            `json:"direction"` // "inbound" or "outbound"
	Filters   []FilterConfig    `json:"filters"`
	Active    bool              `json:"active"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// FilterConfig is the configuration of a single filter step in a pipeline.
type FilterConfig struct {
	Name        string          `json:"name"`
	Type        FilterType      `json:"type"`
	Enabled     bool            `json:"enabled"`
	Unskippable bool            `json:"unskippable,omitempty"`
	Config      json.RawMessage `json:"config,omitempty"`
}

// ── Custom Filter Definitions ───────────────────────────────────────

// CustomFilterDef is a user-created filter definition (JSON config).
type CustomFilterDef struct {
	Name          string      `json:"name"`
	Description   string      `json:"description"`
	Type          FilterType  `json:"type"`
	Direction     string      `json:"direction"`
	Condition     *Condition  `json:"condition,omitempty"`
	Action        Action      `json:"action,omitempty"`
	RejectMessage string      `json:"reject_message,omitempty"`
	Transform     *Transform  `json:"transform,omitempty"`
	Webhook       *WebhookCfg `json:"webhook,omitempty"`
}

// Condition defines matching rules for custom filters.
type Condition struct {
	Match   string `json:"match"`   // JSON path expression
	Pattern string `json:"pattern"` // glob or regex pattern
}

// Transform defines modifications for custom transform filters.
type Transform struct {
	AddHeader       map[string]string `json:"add_header,omitempty"`
	RemoveHeader    []string          `json:"remove_header,omitempty"`
	AppendBody      string            `json:"append_body,omitempty"`
	DeliverToFolder string            `json:"deliver_to_folder,omitempty"`
}

// WebhookCfg defines webhook configuration for action filters.
type WebhookCfg struct {
	URL             string            `json:"url"`
	Method          string            `json:"method,omitempty"` // default POST
	Headers         map[string]string `json:"headers,omitempty"`
	PayloadTemplate interface{}       `json:"payload_template,omitempty"`
	TimeoutMS       int               `json:"timeout_ms,omitempty"` // default 5000
}
