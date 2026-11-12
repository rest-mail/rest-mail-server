package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
	"unsafe"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

// JSONB is a custom type for PostgreSQL JSONB columns.
type JSONB json.RawMessage

func (j JSONB) Value() (driver.Value, error) {
	if len(j) == 0 {
		return "[]", nil
	}
	// A view, because the column can be as long as a header a sender wrote, and the driver only
	// reads it while a statement runs, and nothing writes to a JSONB in that time.
	return unsafe.String(unsafe.SliceData(j), len(j)), nil
}

func (j *JSONB) Scan(value interface{}) error {
	if value == nil {
		*j = JSONB("[]")
		return nil
	}
	switch v := value.(type) {
	case []byte:
		*j = JSONB(v)
	case string:
		*j = JSONB(v)
	default:
		return fmt.Errorf("unsupported type: %T", value)
	}
	return nil
}

func (j JSONB) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("[]"), nil
	}
	return []byte(j), nil
}

func (j *JSONB) UnmarshalJSON(data []byte) error {
	*j = JSONB(data)
	return nil
}

// MessageReplyParentSQL is the Message-ID a stored message answers: its In-Reply-To, or the
// last of its References when that is absent, without the brackets a stored spelling may or
// may not have. It is cut to 255 characters because idx_messages_reply_parent indexes it and
// References is unbounded: a longer value would make a B-tree refuse the message outright.
// prefix names the row, as "reply." does; the index is built with none.
func MessageReplyParentSQL(prefix string) string {
	return `left(btrim(COALESCE(NULLIF(` + prefix + `in_reply_to, ''), (regexp_match(` + prefix + `"references", '([^\s<>]+)[\s<>]*$'))[1], ''), '<> '), 255)`
}

// LocalRefusal is one recipient on this server that refused a sent message, and the kind
// of refusal. What the refusing filter said is not kept: only the category reaches the
// sender.
type LocalRefusal struct {
	Address  string          `json:"address"`
	Category FailureCategory `json:"category"`
}

type Message struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	MailboxID uint   `gorm:"not null;index:idx_messages_mailbox_folder;index:idx_messages_mailbox_deleted" json:"mailbox_id"`
	Folder    string `gorm:"size:255;not null;default:INBOX;index:idx_messages_mailbox_folder" json:"folder"`
	// The RFC 5322 Message-ID header (a string). Careful: the column name
	// message_id doubles as GORM's conventional FK-column name for relations
	// referencing Message, so an ambiguous association elsewhere can hijack
	// this column during AutoMigrate — see the note on OutboundQueue.
	MsgID        string     `gorm:"column:message_id;size:995;index" json:"message_id"`
	InReplyTo    string     `gorm:"size:995" json:"in_reply_to"`
	References   string     `gorm:"type:text" json:"references"`
	ThreadID     string     `gorm:"size:995;index" json:"thread_id"`
	Due          *time.Time `gorm:"->;-:migration" json:"-"`
	DueSource    *string    `gorm:"->;-:migration" json:"-"`
	SnoozedUntil *time.Time `gorm:"->;-:migration" json:"-"`
	Sender       string     `gorm:"size:255;not null" json:"sender"`
	SenderName   string     `gorm:"size:255" json:"sender_name"`
	RecipientsTo JSONB      `gorm:"type:jsonb;not null;default:'[]'" json:"recipients_to"`
	RecipientsCc JSONB      `gorm:"type:jsonb;not null;default:'[]'" json:"recipients_cc"`
	Subject      string     `gorm:"type:text" json:"subject"`
	BodyText     string     `gorm:"type:text" json:"body_text,omitempty"`
	BodyHTML     string     `gorm:"type:text" json:"body_html,omitempty"`
	Headers      JSONB      `gorm:"type:jsonb" json:"headers,omitempty"`
	RawMessage   string     `gorm:"type:text" json:"-"`
	AuthResults  JSONB      `gorm:"type:jsonb;default:null" json:"-"`
	SealedHash   string     `gorm:"size:64;default:null" json:"sealed_hash,omitempty"`
	// LocalRefusals are the recipients on this server that refused this Sent copy when it
	// was sent, as a JSON array of LocalRefusal. A refused local recipient leaves no queue
	// row and no delivered copy, and is not tried again, so this is its only record (#1899).
	// NULL when none refused.
	LocalRefusals JSONB `gorm:"type:jsonb;default:null" json:"-"`
	// RuleClass is the class a Sieve rule assigned at delivery, recorded once with the message
	// so the answer stays the same when the script later changes. NULL on every row delivered
	// before this column existed, with no backfill, and when no rule assigned one.
	RuleClass    string `gorm:"size:16;default:null" json:"-"`
	PendingDraft bool   `gorm:"-" json:"-"`
	// RawOctets preserves sources that PostgreSQL text cannot represent. RawMessage is empty
	// then: Source and SourceText read whichever holds the message.
	RawOctets         []byte `gorm:"type:bytea" json:"-"`
	CalendarEventsRaw JSONB  `gorm:"column:calendar_events;type:jsonb" json:"calendar_events,omitempty"`
	SizeBytes         int    `gorm:"not null;default:0" json:"size_bytes"`
	// RawSize is the exact octet count of RawMessage as stored — the size a
	// protocol client receives when the raw is served verbatim. IMAP
	// RFC822.SIZE (RFC 3501) and POP3 STAT/LIST must report the transmitted
	// octet count exactly, which SizeBytes (a quota heuristic over
	// subject/body lengths) does not guarantee. Zero means "no stored raw";
	// consumers fall back to SizeBytes.
	RawSize int `gorm:"not null;default:0" json:"raw_size"`
	// DowngradedSize is the octet count a POP3 client that has not enabled UTF-8
	// receives: the stored raw with its header block downgraded to ASCII (RFC
	// 6857) and a top-level 8-bit boundary respelled, see rmail.DowngradeForPOP3,
	// with every line ending in CRLF as RETR sends it. POP3UTF8Size is the same
	// count for a client that has enabled UTF-8, which gets the headers as stored.
	// Both equal RawSize when the headers are already ASCII, every line ends in
	// CRLF and the last line has one. STAT and LIST report them, because the size
	// must count the octets actually sent (RFC 1939 section 5). BeforeCreate measures
	// both for every stored raw, so zero means there is no stored raw.
	DowngradedSize int `gorm:"not null;default:0" json:"downgraded_size"`
	POP3UTF8Size   int `gorm:"column:pop3_utf8_size;not null;default:0" json:"pop3_utf8_size"`
	// IMAPSize and IMAPUTF8Size are the octet counts an IMAP FETCH of the whole
	// message sends, without and with UTF-8 enabled: FETCH converts a message to
	// 7-bit on the way out (RFC 3501 section 7.4.2) and RawMessage never changes.
	// RFC822.SIZE reports them, so a client's size matches what it receives
	// without the message being loaded for every metadata FETCH. They equal
	// RawSize when the message is already 7-bit, and are zero only when no raw
	// source is stored.
	IMAPSize     int `gorm:"column:imap_size;not null;default:0" json:"imap_size"`
	IMAPUTF8Size int `gorm:"column:imap_utf8_size;not null;default:0" json:"imap_utf8_size"`
	// ReceivedTLS records the inbound transport security of an inbound-MX
	// (port 25) delivery: true = the connection was encrypted (implicit TLS or
	// STARTTLS), false = plaintext. It is a pointer so NULL can mean "not an
	// inbound-MX delivery / unknown" (local webmail send, IMAP APPEND,
	// authenticated submission, and every row created before this column
	// existed) — kept distinct from a real plaintext arrival so the dashboard's
	// inbound-MX denominator (received_tls IS NOT NULL) and the plaintext count
	// stay correct. Always collected for inbound MX; there is no toggle.
	ReceivedTLS *bool `gorm:"index" json:"received_tls,omitempty"`
	// RequireTLS is the REQUIRETLS tag the message arrived with (RFC 8689 §4.1): any copy
	// of it REST Mail sends on, a Sieve redirect or a forward of the message itself, goes
	// with TLS required too.
	RequireTLS bool `gorm:"not null;default:false" json:"-"`
	// SubmittedAt is when authenticated SMTP submission saved this message to the
	// sender's Sent. NULL on a message that came any other way. An IMAP APPEND reads it
	// to tell whether a mail app's own copy is of a message already saved (#1707).
	SubmittedAt *time.Time `gorm:"default:null" json:"-"`
	// TLSVersion is the negotiated TLS version label (e.g. "TLS1.3") for an
	// encrypted inbound-MX delivery; empty when plaintext or not applicable.
	TLSVersion     string `gorm:"size:16" json:"tls_version,omitempty"`
	HasAttachments bool   `gorm:"default:false" json:"has_attachments"`
	IsAnswered     bool   `gorm:"default:false" json:"is_answered,omitempty"`
	IsRead         bool   `gorm:"default:false" json:"is_read"`
	// IsDSN marks a message this server generated as a Delivery Status
	// Notification (a mailer-daemon bounce). The outbound worker counts only these
	// rows for the per-recipient DSN rate limit, so inbound mail that merely claims
	// a mailer-daemon@ envelope-from cannot inflate the counter and suppress a
	// target user's legitimate bounces.
	IsDSN       bool     `gorm:"default:false" json:"is_dsn"`
	IsFlagged   bool     `gorm:"default:false" json:"is_flagged"`
	IsStarred   bool     `gorm:"default:false" json:"is_starred"`
	Keywords    []string `gorm:"serializer:json;type:jsonb" json:"keywords,omitempty"`
	IMAPDeleted bool     `gorm:"not null;default:false" json:"imap_deleted,omitempty"`
	// ModSeq is the message's IMAP mod-sequence (RFC 7162 §3.1). A trigger sets it
	// from the folder's FolderModSeq on every insert, move and visible flag
	// change; nothing else writes it.
	ModSeq     uint64     `gorm:"column:mod_seq;not null;default:1" json:"-"`
	IsDraft    bool       `gorm:"default:false" json:"is_draft"`
	IsDeleted  bool       `gorm:"default:false;index:idx_messages_mailbox_deleted" json:"is_deleted"`
	ReceivedAt time.Time  `gorm:"default:now();index" json:"received_at"`
	DateHeader *time.Time `json:"date_header"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`

	// AwaitingSince is derived on read from the thread's messages and never stored.
	AwaitingSince *time.Time `gorm:"-" json:"-"`

	// Associations
	Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"mailbox,omitempty"`
}

func (Message) TableName() string { return "messages" }

// BeforeCreate measures the sizes a gateway reports for a message stored with its
// raw source, on every path that stores one, so no delivery path can forget them.
func (m *Message) BeforeCreate(tx *gorm.DB) error {
	returnStoredModSeq(tx)
	m.fitColumns()
	if m.RawOctets == nil {
		m.RawMessage, m.RawOctets = SourceColumns(m.RawMessage)
	}
	for _, text := range []*string{&m.MsgID, &m.InReplyTo, &m.References, &m.ThreadID, &m.Sender, &m.SenderName, &m.Subject, &m.BodyText, &m.BodyHTML} {
		*text = ReadableText(*text)
	}
	for _, list := range []*JSONB{&m.RecipientsTo, &m.RecipientsCc, &m.Headers, &m.CalendarEventsRaw} {
		*list = readableJSON(*list)
	}
	if m.RawSize > 0 && (m.DowngradedSize == 0 || m.POP3UTF8Size == 0 || m.IMAPSize == 0 || m.IMAPUTF8Size == 0) {
		sizes := rmail.MeasureWireSizes(m.sourceView())
		if m.DowngradedSize == 0 {
			m.DowngradedSize = sizes.Downgraded
		}
		if m.POP3UTF8Size == 0 {
			m.POP3UTF8Size = sizes.POP3UTF8
		}
		if m.IMAPSize == 0 {
			m.IMAPSize = sizes.IMAP
		}
		if m.IMAPUTF8Size == 0 {
			m.IMAPUTF8Size = sizes.IMAPUTF8
		}
	}
	if m.PendingDraft || (m.IsDeleted && m.Sender == "system" && m.Subject == "" && m.BodyText == "" && m.BodyHTML == "" && m.RawMessage == "" && m.RawOctets == nil) {
		m.SealedHash = ""
		return nil
	}
	var err error
	m.SealedHash, err = m.SealDigest()
	return err
}

// sourceView is the source as a string without copying it, for reading it while nothing
// else writes to it: measuring a message must not cost a copy of it. Nothing may keep it.
func (m *Message) sourceView() string {
	if m.RawOctets != nil {
		return unsafe.String(unsafe.SliceData(m.RawOctets), len(m.RawOctets))
	}
	return m.RawMessage
}

// returnStoredModSeq adds mod_seq to the INSERT's RETURNING. The trigger sets
// it before the row is written, and GORM reads back only the columns it thinks
// the database fills, which a column with a Go default is not. GORM adds its
// own RETURNING (the ID) only when none is there yet, so those columns are kept
// in the one this adds. A batch calls this once per row; the first adds it.
func returnStoredModSeq(tx *gorm.DB) {
	if tx == nil {
		return
	}
	stmt := tx.Statement
	if stmt == nil || stmt.Schema == nil {
		return
	}
	modSeq := clause.Column{Name: "mod_seq"}
	if existing, ok := stmt.Clauses["RETURNING"]; ok {
		returning, _ := existing.Expression.(clause.Returning)
		if returning.Columns == nil {
			return // RETURNING * already reads it
		}
		for _, column := range returning.Columns {
			if column.Name == modSeq.Name {
				return
			}
		}
		stmt.AddClause(clause.Returning{Columns: []clause.Column{modSeq}})
		return
	}
	columns := make([]clause.Column, 0, len(stmt.Schema.FieldsWithDefaultDBValue)+1)
	for _, field := range stmt.Schema.FieldsWithDefaultDBValue {
		if field.Readable {
			columns = append(columns, clause.Column{Name: field.DBName})
		}
	}
	stmt.AddClause(clause.Returning{Columns: append(columns, modSeq)})
}

// Source returns original octets, including historical non-UTF-8 and NUL data.
func (m Message) Source() []byte {
	if m.RawOctets != nil {
		return m.RawOctets
	}
	return []byte(m.RawMessage)
}

// SourceText is Source as a string, with no copy of a source stored as text.
func (m Message) SourceText() string {
	if m.RawOctets != nil {
		return string(m.RawOctets)
	}
	return m.RawMessage
}
