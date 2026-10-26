package handlers

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	apitypes "github.com/restmail/restmail/internal/api/types"
	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// MessageImportHandler stores historical mail independently of delivery:
// no header rewriting, spam/Sieve rules, forwarding, vacation or push notification.
type MessageImportHandler struct {
	db        *gorm.DB
	maxBytes  int64
	store     *attachmentstore.S3
	directory string
}

func NewMessageImportHandler(db *gorm.DB, maxBytes int64, store *attachmentstore.S3) *MessageImportHandler {
	if maxBytes <= 0 {
		maxBytes = config.DefaultSMTPMaxMessageSize
	}
	return &MessageImportHandler{db: db, maxBytes: maxBytes, store: store, directory: attachmentstore.FilesystemRoot}
}

type importMessageRequest struct {
	ID   uint32                     `path:"id" minimum:"1" example:"1"`
	Body apitypes.ImportMessageBody `body:""`
}

func (h *MessageImportHandler) Register(rs endpoint.Routes, checks MailboxRouteChecks) {
	// Bound JSON before decoding, even when the general API body cap is off.
	// Base64 expands by 4/3; the remaining allowance bounds folder/flag metadata.
	limit := int64(math.MaxInt64)
	if h.maxBytes <= (math.MaxInt64-65536)/4*3 {
		limit = (h.maxBytes+2)/3*4 + 65536
	}
	cap := endpoint.Middleware{Wrap: middleware.MaxBodyBytes(limit), Describes: oapifly.Middleware{
		Responses: []oapifly.EndpointResponse{{Status: http.StatusRequestEntityTooLarge,
			Description: "request_too_large: the encoded import exceeds the request body limit", Type: oapifly.TypeOf[respond.ErrorResponse]()}},
	}}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/messages/import",
		OperationID: "importMessage", Summary: "Import one historical message", Tags: []string{"Admin: Mailboxes"},
		Description: "Stores original message octets, folder, supported system flags and required internal_date without delivery rules or notifications. Requires mailboxes:write on the destination. A Message-ID already present in that account and folder is skipped without changing its flags or date. Messages without Message-ID use a deterministic SHA-256 import identity; the raw source remains unchanged. Every attempt is audited; successful imports and skips also receive a transactional messages.import audit entry. The decoded message is bounded by SMTP_MAX_MESSAGE_SIZE (10 MiB by default); the general API body limit may be stricter. Deleted-state and keyword flags are unsupported and refused rather than hidden or discarded.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge},
		Middleware:  []endpoint.Middleware{checks.OnMailbox(middleware.CapMailboxesWrite), cap}}, h.importMessage)
}

var errImportQuota = endpoint.Refuse(http.StatusConflict, "mailbox_full", "The destination account has insufficient quota")

func (h *MessageImportHandler) importMessage(r *http.Request, in importMessageRequest) (apitypes.ImportMessageResult, error) {
	var result apitypes.ImportMessageResult
	body := in.Body
	if int64(len(body.RawMessage)) > h.maxBytes {
		return result, endpoint.Refuse(http.StatusRequestEntityTooLarge, "message_too_large", "The decoded message exceeds the import size limit")
	}
	if body.Folder == "" || strings.EqualFold(body.Folder, "INBOX") {
		body.Folder = "INBOX"
	}
	problems := map[string]string{}
	if problem := rmail.FolderNameProblem(body.Folder); problem != "" {
		problems["folder"] = problem
	}
	if len(body.RawMessage) == 0 {
		problems["raw_message"] = "required: a complete RFC 5322 message"
	}
	if body.InternalDate.IsZero() || body.InternalDate.Year() < 1 || body.InternalDate.Year() > 9999 {
		problems["internal_date"] = "required: the original internal date as an RFC 3339 timestamp"
	}
	msg := models.Message{MailboxID: uint(in.ID), Folder: body.Folder, ReceivedAt: body.InternalDate}
	for _, flag := range body.Flags {
		switch strings.ToLower(flag) {
		case `\seen`:
			msg.IsRead = true
		case `\answered`:
			msg.IsAnswered = true
		case `\flagged`:
			msg.IsFlagged = true
		case `\draft`:
			msg.IsDraft = true
		default:
			problems["flags"] = "unsupported flag: " + flag
		}
	}
	if len(problems) != 0 {
		return result, endpoint.Invalid(problems)
	}
	parsed, err := rmime.Parse(body.RawMessage)
	if err != nil {
		return result, endpoint.Invalid(map[string]string{"raw_message": "must be a parseable RFC 5322 message"})
	}
	msg.MsgID = rmail.CanonicalID(parsed.Headers.MessageID)
	if msg.MsgID == "" || !utf8.ValidString(msg.MsgID) || strings.ContainsRune(msg.MsgID, 0) {
		msg.MsgID = fmt.Sprintf("<import.%x@rest-mail.invalid>", sha256.Sum256(body.RawMessage))
	}
	if len(msg.MsgID) > 995 {
		return result, endpoint.Invalid(map[string]string{"raw_message": "Message-ID exceeds 995 bytes"})
	}
	msg.RawSize = len(body.RawMessage)
	if utf8.Valid(body.RawMessage) && !strings.ContainsRune(string(body.RawMessage), 0) {
		msg.RawMessage = string(body.RawMessage)
	} else {
		msg.RawOctets = body.RawMessage
	}
	text := func(value string) string { return strings.ToValidUTF8(strings.ReplaceAll(value, "\x00", "�"), "�") }
	msg.Subject = text(parsed.Headers.Subject)
	msg.BodyText, msg.BodyHTML = rmime.TextAndHTML(parsed.Body)
	msg.BodyText, msg.BodyHTML = text(msg.BodyText), text(msg.BodyHTML)
	msg.SizeBytes = len(msg.Subject) + len(msg.BodyText) + len(msg.BodyHTML)
	msg.InReplyTo = text(rmail.CanonicalID(parsed.Headers.InReplyTo))
	msg.References = text(strings.Join(parsed.Headers.References, " "))
	msg.ThreadID = msg.MsgID
	if len(parsed.Headers.References) != 0 {
		msg.ThreadID = text(rmail.CanonicalID(parsed.Headers.References[0]))
	} else if msg.InReplyTo != "" {
		msg.ThreadID = msg.InReplyTo
	}
	if len(parsed.Headers.From) != 0 {
		msg.Sender, msg.SenderName = text(parsed.Headers.From[0].Address), text(parsed.Headers.From[0].Name)
	}
	addresses := func(items []pipeline.Address) models.JSONB {
		list := make([]string, 0, len(items))
		for _, item := range items {
			list = append(list, item.Address)
		}
		encoded, _ := json.Marshal(list)
		return models.JSONB(encoded)
	}
	msg.RecipientsTo, msg.RecipientsCc = addresses(parsed.Headers.To), addresses(parsed.Headers.Cc)
	headers, _ := json.Marshal(parsed.Headers.Raw)
	msg.Headers = models.JSONB(headers)
	if date, err := mail.ParseDate(parsed.Headers.Date); err == nil {
		msg.DateHeader = &date
	}
	if len(parsed.CalendarEvents) != 0 {
		data, _ := json.Marshal(parsed.CalendarEvents)
		msg.CalendarEventsRaw = models.JSONB(data)
	}
	ctx := r.Context()
	err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize only imports sharing this exact destination/identity. Do
		// not add a global Message-ID uniqueness rule to ordinary delivery.
		key := sha256.Sum256([]byte(fmt.Sprintf("restmail/import/%d/%s/%s", in.ID, body.Folder, msg.MsgID)))
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(binary.BigEndian.Uint64(key[:8]))).Error; err != nil {
			return err
		}
		var box models.Mailbox
		if err := tx.First(&box, in.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return endpoint.Refuse(http.StatusNotFound, "mailbox_not_found", "Destination account not found")
			}
			return err
		}
		var existing models.Message
		err := tx.Where("mailbox_id = ? AND folder = ? AND message_id IN ?", in.ID, body.Folder,
			[]string{msg.MsgID, strings.Trim(msg.MsgID, "<>")}).Order("id").First(&existing).Error
		switch {
		case err == nil:
			result = apitypes.ImportMessageResult{ID: existing.ID, MessageID: existing.MsgID, Folder: existing.Folder}
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		default:
			var rows []models.Attachment
			var attachmentBytes int64
			for _, group := range [][]pipeline.Attachment{parsed.Attachments, parsed.Inline} {
				for _, att := range group {
					data, err := base64.StdEncoding.DecodeString(att.Content)
					if err != nil {
						return fmt.Errorf("decode imported attachment: %w", err)
					}
					var ref, checksum, storage string
					if h.store != nil {
						ref, checksum, err = h.store.Put(ctx, data)
						storage = "s3"
					} else {
						ref, checksum, err = attachmentstore.PutFilesystem(ctx, h.directory, data)
						storage = "filesystem"
					}
					if err != nil {
						return fmt.Errorf("store imported attachment: %w", err)
					}
					rows = append(rows, models.Attachment{Filename: att.Filename, ContentType: att.ContentType,
						SizeBytes: int64(len(data)), StorageType: storage, StorageRef: ref, Checksum: checksum})
					attachmentBytes += int64(len(data))
				}
			}
			msg.HasAttachments = len(rows) != 0
			if err := tx.Create(&msg).Error; err != nil {
				return err
			}
			for i := range rows {
				rows[i].MessageID = msg.ID
			}
			if len(rows) != 0 {
				if err := tx.Create(&rows).Error; err != nil {
					return err
				}
			}
			usage := tx.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", in.ID).Updates(map[string]any{
				"subject_bytes":    gorm.Expr("subject_bytes + ?", len(msg.Subject)),
				"body_bytes":       gorm.Expr("body_bytes + ?", len(msg.BodyText)+len(msg.BodyHTML)),
				"attachment_bytes": gorm.Expr("attachment_bytes + ?", attachmentBytes),
				"message_count":    gorm.Expr("message_count + 1"),
			})
			if usage.Error != nil {
				return usage.Error
			}
			if usage.RowsAffected != 1 {
				return errors.New("destination quota usage is missing")
			}
			cost := int64(msg.SizeBytes) + attachmentBytes
			quota := tx.Model(&models.Mailbox{}).Where("id = ? AND (quota_bytes <= 0 OR quota_used_bytes + ? <= quota_bytes)", in.ID, cost).
				Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", cost))
			if quota.Error != nil {
				return quota.Error
			}
			if quota.RowsAffected != 1 {
				return errImportQuota
			}
			result = apitypes.ImportMessageResult{ID: msg.ID, Imported: true, MessageID: msg.MsgID, Folder: msg.Folder}
		}
		entry := middleware.NewAuditEntry("messages.import", r, time.Now())
		entry.Status = http.StatusOK
		entry.Target["account_id"] = strconv.FormatUint(uint64(in.ID), 10)
		entry.Target["message_id"] = strconv.FormatUint(uint64(result.ID), 10)
		entry.Target["imported"] = strconv.FormatBool(result.Imported)
		return audit.Append(tx, entry)
	})
	if err != nil {
		return apitypes.ImportMessageResult{}, fmt.Errorf("import historical message: %w", err)
	}
	return result, nil
}
