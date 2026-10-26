package handlers

import (
	"encoding/json"
	"time"

	apitypes "github.com/restmail/restmail/internal/api/types"
	"github.com/restmail/restmail/internal/db/models"
)

// Message is a message in an account as the mail routes answer it: its envelope, its bodies,
// its flags and the folder it is in. The raw source is answered by GET /messages/{id}/raw and
// is never written here. Its fields, their order and their omissions are the model's, so the
// bytes written are unchanged: the IMAP and POP3 gateways, webmail and the mobile app read
// them. The recipients, headers and calendar events are the stored JSON, as stored: what a
// gateway delivered is kept, so the API does not narrow it.
type Message = apitypes.Message

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
		IsAnswered:        m.IsAnswered,
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

// MessageAttachment is a message's attachment as its list answers it: what it is called, its type,
// size and checksum. Where it is stored, a path on the server's disk or an S3 key, stays on the
// server (#658). Its content is answered by GET /attachments/{id}.
type MessageAttachment struct {
	ID          uint      `json:"id"`
	MessageID   uint      `json:"message_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
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
		Checksum:    m.Checksum,
		CreatedAt:   m.CreatedAt,
	}
}
