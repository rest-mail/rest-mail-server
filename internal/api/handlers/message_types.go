package handlers

import (
	"encoding/json"
	"time"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// Message is a message in an account as the mail routes answer it: its envelope, its bodies,
// its flags and the folder it is in. The raw source is answered by GET /messages/{id}/raw and
// is never written here. An unloaded mailbox association is omitted; a loaded one retains
// its public fields. The recipients, headers and calendar events are the stored JSON: what a
// gateway delivered is kept, so the API does not narrow it.
type Message = apitypes.Message

// messageClass is the class of a stored message, worked out on read as the authentication
// results are. Most delivery paths store only the source and no decoded headers, and a long field
// keeps the rest out of the row too, so the source answers when the row has no usable decoded
// headers. Both are the same fields (rmime.SourceHeaders), or a long field could change the class.
func messageClass(m models.Message) string {
	var headers map[string][]string
	if err := json.Unmarshal(m.Headers, &headers); err != nil || len(headers) == 0 {
		headers = rmime.SourceHeaders(m.SourceReader())
	}
	return rmail.Classify(headers, m.RuleClass)
}

func messageOf(m models.Message) Message {
	var mailbox *Mailbox
	if m.Mailbox.ID != 0 {
		loaded := mailboxOf(m.Mailbox)
		mailbox = &loaded
	}
	authResults, authed := pipeline.ParseStoredAuthResults(m.SourceText())
	authVerdict := ""
	if authed {
		authVerdict = pipeline.AuthVerdict(authResults)
	}
	return Message{
		Class:             messageClass(m),
		ID:                m.ID,
		MailboxID:         m.MailboxID,
		Folder:            m.Folder,
		MsgID:             m.MsgID,
		InReplyTo:         m.InReplyTo,
		References:        m.References,
		ThreadID:          m.ThreadID,
		Due:               m.Due,
		DueSource:         m.DueSource,
		SnoozedUntil:      m.SnoozedUntil,
		AwaitingSince:     m.AwaitingSince,
		Sender:            m.Sender,
		SenderName:        m.SenderName,
		RecipientsTo:      storedList(m.RecipientsTo),
		RecipientsCc:      storedList(m.RecipientsCc),
		Subject:           m.Subject,
		BodyText:          m.BodyText,
		BodyHTML:          m.BodyHTML,
		Headers:           json.RawMessage(m.Headers),
		SealedHash:        m.SealedHash,
		CalendarEventsRaw: json.RawMessage(m.CalendarEventsRaw),
		SizeBytes:         m.SizeBytes,
		RawSize:           m.RawSize,
		DowngradedSize:    m.DowngradedSize,
		POP3UTF8Size:      m.POP3UTF8Size,
		IMAPSize:          m.IMAPSize,
		IMAPUTF8Size:      m.IMAPUTF8Size,
		ReceivedTLS:       m.ReceivedTLS,
		TLSVersion:        m.TLSVersion,
		HasAttachments:    m.HasAttachments,
		IsAnswered:        m.IsAnswered,
		Keywords:          m.Keywords,
		IMAPDeleted:       m.IMAPDeleted,
		ModSeq:            m.ModSeq,
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
		Mailbox:           mailbox,
		AuthResults:       authResults,
		AuthVerdict:       authVerdict,
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
	Filename    string    `json:"filename" description:"What the sender called it, for showing: at most 255 characters of valid UTF-8, with U+FFFD for a NUL or an invalid octet. A longer name keeps its start and its last extension. GET /messages/{id}/raw answers the name as it was sent"`
	ContentType string    `json:"content_type" description:"The part's media type, or application/octet-stream when it is not one a media type can be: over 255 characters, or not valid UTF-8"`
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
