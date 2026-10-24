package handlers

import (
	"encoding/json"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// Message is a message in an account as the mail routes answer it: its envelope, its bodies,
// its flags and the folder it is in. The raw source is answered by GET /messages/{id}/raw and
// is never written here. Its fields, their order and their omissions are the model's, so the
// bytes written are unchanged: the IMAP and POP3 gateways, webmail and the mobile app read
// them. The recipients, headers and calendar events are the stored JSON, as stored: what a
// gateway delivered is kept, so the API does not narrow it.
type Message struct {
	ID                uint            `json:"id"`
	MailboxID         uint            `json:"mailbox_id"`
	Folder            string          `json:"folder"`
	MsgID             string          `json:"message_id" description:"The RFC 5322 Message-ID"`
	InReplyTo         string          `json:"in_reply_to"`
	References        string          `json:"references"`
	ThreadID          string          `json:"thread_id"`
	Sender            string          `json:"sender"`
	SenderName        string          `json:"sender_name"`
	RecipientsTo      json.RawMessage `json:"recipients_to"`
	RecipientsCc      json.RawMessage `json:"recipients_cc"`
	Subject           string          `json:"subject"`
	BodyText          string          `json:"body_text,omitempty"`
	BodyHTML          string          `json:"body_html,omitempty"`
	Headers           json.RawMessage `json:"headers,omitempty"`
	CalendarEventsRaw json.RawMessage `json:"calendar_events,omitempty"`
	SizeBytes         int             `json:"size_bytes"`
	RawSize           int             `json:"raw_size" description:"The exact octet count of the stored raw message; 0 when none is stored"`
	ReceivedTLS       *bool           `json:"received_tls,omitempty" description:"For an inbound-MX delivery, whether it arrived encrypted"`
	TLSVersion        string          `json:"tls_version,omitempty"`
	HasAttachments    bool            `json:"has_attachments"`
	IsRead            bool            `json:"is_read"`
	IsDSN             bool            `json:"is_dsn" description:"A delivery status notification this server generated"`
	IsFlagged         bool            `json:"is_flagged"`
	IsStarred         bool            `json:"is_starred"`
	IsDraft           bool            `json:"is_draft"`
	IsDeleted         bool            `json:"is_deleted"`
	ReceivedAt        time.Time       `json:"received_at"`
	DateHeader        *time.Time      `json:"date_header"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Mailbox           Mailbox         `json:"mailbox,omitempty" description:"Always the empty mailbox: the association is never loaded on these routes"`
}

func messageOf(m models.Message) Message {
	return Message{
		ID:                m.ID,
		MailboxID:         m.MailboxID,
		Folder:            m.Folder,
		MsgID:             m.MsgID,
		InReplyTo:         m.InReplyTo,
		References:        m.References,
		ThreadID:          m.ThreadID,
		Sender:            m.Sender,
		SenderName:        m.SenderName,
		RecipientsTo:      storedList(m.RecipientsTo),
		RecipientsCc:      storedList(m.RecipientsCc),
		Subject:           m.Subject,
		BodyText:          m.BodyText,
		BodyHTML:          m.BodyHTML,
		Headers:           json.RawMessage(m.Headers),
		CalendarEventsRaw: json.RawMessage(m.CalendarEventsRaw),
		SizeBytes:         m.SizeBytes,
		RawSize:           m.RawSize,
		ReceivedTLS:       m.ReceivedTLS,
		TLSVersion:        m.TLSVersion,
		HasAttachments:    m.HasAttachments,
		IsRead:            m.IsRead,
		IsDSN:             m.IsDSN,
		IsFlagged:         m.IsFlagged,
		IsStarred:         m.IsStarred,
		IsDraft:           m.IsDraft,
		IsDeleted:         m.IsDeleted,
		ReceivedAt:        m.ReceivedAt,
		DateHeader:        m.DateHeader,
		CreatedAt:         m.CreatedAt,
		UpdatedAt:         m.UpdatedAt,
		Mailbox:           mailboxOf(m.Mailbox),
	}
}

// storedList is a stored JSON list as the model writes it: [] when nothing is stored.
func storedList(raw models.JSONB) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return json.RawMessage(raw)
}

func messagesOf(rows []models.Message) []Message {
	out := make([]Message, len(rows))
	for i, row := range rows {
		out[i] = messageOf(row)
	}
	return out
}

// MessageAttachment is a message's attachment as its list answers it: what it is called, its type
// and size, and where it is stored. Its content is answered by GET /attachments/{id}.
type MessageAttachment struct {
	ID          uint      `json:"id"`
	MessageID   uint      `json:"message_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	StorageType string    `json:"storage_type" enums:"filesystem,s3"`
	StorageRef  string    `json:"storage_ref"`
	Checksum    string    `json:"checksum" description:"SHA-256 hex"`
	CreatedAt   time.Time `json:"created_at"`
}

func attachmentOf(m models.Attachment) MessageAttachment {
	return MessageAttachment{
		ID:          m.ID,
		MessageID:   m.MessageID,
		Filename:    m.Filename,
		ContentType: m.ContentType,
		SizeBytes:   m.SizeBytes,
		StorageType: m.StorageType,
		StorageRef:  m.StorageRef,
		Checksum:    m.Checksum,
		CreatedAt:   m.CreatedAt,
	}
}
