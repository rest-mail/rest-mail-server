package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	oapifly "github.com/antimatter-studios/go-oapifly"
	goimap "github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

// MessageImportHandler stores historical mail independently of delivery:
// no header rewriting, spam/Sieve rules, forwarding, vacation or push notification.
type MessageImportHandler struct {
	db        *gorm.DB
	maxBytes  int64
	store     *attachmentstore.S3
	broker    messageEventPublisher
	directory string
}

func NewMessageImportHandler(db *gorm.DB, maxBytes int64, store *attachmentstore.S3, broker messageEventPublisher) *MessageImportHandler {
	if maxBytes <= 0 {
		maxBytes = config.DefaultSMTPMaxMessageSize
	}
	return &MessageImportHandler{db: db, maxBytes: maxBytes, store: store, broker: broker, directory: attachmentstore.FilesystemRoot}
}

// importMessageRequest is the request as the document describes it and the binder judges it. The
// route reads the body itself (importSpooledMessage), so the message is never in Body.RawMessage
// except for the rare forms named there.
type importMessageRequest struct {
	ID   uint32                     `path:"id" minimum:"1" example:"1"`
	Body apitypes.ImportMessageBody `body:""`
}

// Register adds the operation to the mail routes. The handler checks mail:import
// on the account, after refusing a body that could never be stored.
func (h *MessageImportHandler) Register(rs endpoint.Routes) {
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
		OperationID: "importMessage", Summary: "Store a message the account already has", Tags: []string{"Messages"},
		Description: "Stores original message octets, folder, supported system flags and required internal_date without delivery rules or push notifications. Requires mail:import on the destination, which every account is granted on its own mailbox by the importer role; an owner can revoke that grant, and the account is then refused with 403. A message whose Message-ID and exact octets are already in that account and folder is skipped without changing its flags or date; the same Message-ID with different octets is stored as another message. Messages without Message-ID use a deterministic SHA-256 import identity; the raw source remains unchanged. Every attempt is audited; successful imports and skips also receive a transactional messages.import audit entry. The decoded message is bounded by SMTP_MAX_MESSAGE_SIZE (10 MiB by default); the general API body limit may be stricter. Deleted state and up to 100 case-insensitive keywords of 1–255 ASCII bytes are preserved. Keywords must be RFC 3501 atoms without backslash. Deleted messages use IMAP deletion state, not API tombstones; EXPUNGE removes them.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge},
		Security:    handlerChecked(models.CapMailImport, folderSchemes...),
		Middleware:  []endpoint.Middleware{cap}, DescribedRequest: importMessageRequest{}}, h.importSpooledMessage)
}

// importRefusal is how an import names a refused account: one the caller may not
// know of is not found, like one that does not exist.
func importRefusal(err error) error {
	switch {
	case errors.Is(err, errNoAccess):
		return endpoint.Refuse(http.StatusNotFound, "mailbox_not_found", "Destination account not found")
	case errors.Is(err, errNotAllowed):
		return endpoint.Refuse(http.StatusForbidden, "forbidden", "Insufficient permissions")
	}
	return err
}

var errImportQuota = endpoint.Refuse(http.StatusConflict, "mailbox_full", "The destination account has insufficient quota")

// storeImportedMessage stores a message from its file. Only headers are read before it is known
// the message must be stored: a refused message and a rerun of one already here read no more of
// it than that. Reading past them happens only then, attachments going straight to their store.
func (h *MessageImportHandler) storeImportedMessage(r *http.Request, accountID uint32, body apitypes.ImportMessageBody, source *importSource) (apitypes.ImportMessageResult, error) {
	var result apitypes.ImportMessageResult
	if source.tooLarge() {
		return result, endpoint.Refuse(http.StatusRequestEntityTooLarge, "message_too_large", "The decoded message exceeds the import size limit")
	}
	if body.Folder == "" || strings.EqualFold(body.Folder, "INBOX") {
		body.Folder = "INBOX"
	}
	problems := map[string]string{}
	if problem := rmail.FolderNameProblem(body.Folder); problem != "" {
		problems["folder"] = problem
	}
	if source.size == 0 {
		problems["raw_message"] = "required: a complete RFC 5322 message"
	}
	if body.InternalDate.IsZero() || body.InternalDate.Year() < 1 || body.InternalDate.Year() > 9999 {
		problems["internal_date"] = "required: the original internal date as an RFC 3339 timestamp"
	}
	msg := models.Message{MailboxID: uint(accountID), Folder: body.Folder, ReceivedAt: body.InternalDate}
	flags, flagErr := goimap.NormalizeImportFlags(body.Flags)
	if flagErr != nil {
		problems["flags"] = flagErr.Error()
	}
	for _, flag := range flags {
		switch strings.ToLower(flag) {
		case `\seen`:
			msg.IsRead = true
		case `\answered`:
			msg.IsAnswered = true
		case `\flagged`:
			// \\Flagged is backed by both columns, as the IMAP gateway maps it.
			msg.IsFlagged, msg.IsStarred = true, true
		case `\draft`:
			msg.IsDraft = true
		case `\deleted`:
			msg.IMAPDeleted = true
		default:
			msg.Keywords = append(msg.Keywords, flag)
		}
	}
	if len(problems) != 0 {
		return result, endpoint.Invalid(problems)
	}
	claims := middleware.GetClaims(r)
	if claims == nil {
		return result, endpoint.Refuse(http.StatusUnauthorized, "unauthorized", "Authentication required")
	}
	if _, err := resolveAccountMailbox(h.db, uint(accountID), claims, models.CapMailImport); err != nil {
		return result, importRefusal(err)
	}
	reader, err := source.reader()
	if err != nil {
		return result, err
	}
	headers, err := rmime.ParseHeaders(reader)
	if err != nil {
		return result, endpoint.Invalid(map[string]string{"raw_message": "must be a parseable RFC 5322 message"})
	}
	msg.MsgID = rmail.CanonicalID(headers.MessageID)
	if msg.MsgID == "" || !utf8.ValidString(msg.MsgID) || strings.ContainsRune(msg.MsgID, 0) {
		msg.MsgID = fmt.Sprintf("<import.%x@rest-mail.invalid>", source.checksum)
	}
	if len(msg.MsgID) > 995 {
		return result, endpoint.Invalid(map[string]string{"raw_message": "Message-ID exceeds 995 bytes"})
	}
	msg.RawSize = int(source.size)
	msg.Subject = importedText(headers.Subject)
	msg.InReplyTo = importedText(rmail.CanonicalID(headers.InReplyTo))
	msg.References = importedText(strings.Join(headers.References, " "))
	if len(headers.From) != 0 {
		msg.Sender, msg.SenderName = importedText(headers.From[0].Address), importedText(headers.From[0].Name)
	}
	addresses := func(items []pipeline.Address) models.JSONB {
		list := make([]string, 0, len(items))
		for _, item := range items {
			list = append(list, item.Address)
		}
		encoded, _ := json.Marshal(list)
		return models.JSONB(encoded)
	}
	msg.RecipientsTo, msg.RecipientsCc = addresses(headers.To), addresses(headers.Cc)
	encodedHeaders, _ := json.Marshal(headers.Raw)
	msg.Headers = models.JSONB(encodedHeaders)
	if date, err := mail.ParseDate(headers.Date); err == nil {
		msg.DateHeader = &date
	}
	ctx := r.Context()
	err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize only imports sharing this exact destination/identity. Do
		// not add a global Message-ID uniqueness rule to ordinary delivery.
		key := sha256.Sum256([]byte(fmt.Sprintf("restmail/import/%d/%s/%s", accountID, body.Folder, msg.MsgID)))
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(binary.BigEndian.Uint64(key[:8]))).Error; err != nil {
			return err
		}
		var box models.Mailbox
		if err := tx.First(&box, accountID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return endpoint.Refuse(http.StatusNotFound, "mailbox_not_found", "Destination account not found")
			}
			return err
		}
		// The same Message-ID with the same octets is this message again, so a
		// rerun skips it. Different octets are a different message that shares an
		// ID (an edited draft, a badly made ID), so it is stored.
		var sameID []storedImportSource
		err := tx.Model(&models.Message{}).Select(storedImportSourceColumns).
			Where("mailbox_id = ? AND folder = ? AND message_id IN ?", accountID, body.Folder,
				[]string{msg.MsgID, strings.Trim(msg.MsgID, "<>")}).Order("id").Find(&sameID).Error
		existing := sameContent(sameID, source.checksum)
		switch {
		case err != nil:
			return err
		case existing != nil:
			result = apitypes.ImportMessageResult{ID: existing.ID, MessageID: existing.MsgID, Folder: existing.Folder}
		default:
			msg.ThreadID, err = repositories.ResolveThreadID(tx, uint(accountID), msg.MsgID, msg.InReplyTo, msg.References)
			if err != nil {
				return err
			}
			reader, err := source.reader()
			if err != nil {
				return err
			}
			// The text columns keep a part's whole text, so no part is cut short here.
			parsed, err := rmime.ParseStream(ctx, reader, rmime.StreamOptions{Store: h.storeAttachmentPart, MaxText: math.MaxInt64})
			if errors.Is(err, rmime.ErrStructureLimit) {
				// Imported mail is held to the same MIME limits as arriving mail (#1641). Its
				// Received: fields are not counted: old mail may carry any number, and storing
				// it in the account is not a hop that could loop (#1647).
				return endpoint.Invalid(map[string]string{"raw_message": "MIME structure over limit: nested too deep or too many parts"})
			}
			if err != nil {
				return fmt.Errorf("store imported attachment: %w", err)
			}
			msg.BodyText, msg.BodyHTML = rmime.TextAndHTML(parsed.Body)
			msg.BodyText, msg.BodyHTML = importedText(msg.BodyText), importedText(msg.BodyHTML)
			msg.SizeBytes = len(msg.Subject) + len(msg.BodyText) + len(msg.BodyHTML)
			if len(parsed.CalendarEvents) != 0 {
				data, _ := json.Marshal(parsed.CalendarEvents)
				msg.CalendarEventsRaw = models.JSONB(data)
			}
			var rows []models.Attachment
			var attachmentBytes int64
			for _, group := range [][]pipeline.Attachment{parsed.Attachments, parsed.Inline} {
				for _, att := range group {
					rows = append(rows, models.Attachment{Filename: att.Filename, ContentType: att.ContentType,
						SizeBytes: att.Size, StorageType: att.Storage, StorageRef: att.Ref, Checksum: att.Checksum})
					attachmentBytes += att.Size
				}
			}
			if err := repositories.ReserveStorage(tx, uint(accountID), int64(msg.SizeBytes)+attachmentBytes); err != nil {
				if errors.Is(err, repositories.ErrStorageQuota) {
					return errImportQuota
				}
				return err
			}
			msg.HasAttachments = len(rows) != 0
			// The row keeps the whole source in raw_message or raw_octets, so this is the one
			// step that reads it all, and only for a message that is being stored.
			if msg.RawMessage, msg.RawOctets, err = source.columns(); err != nil {
				return err
			}
			if err := tx.Create(&msg).Error; err != nil {
				return err
			}
			msg.RawMessage, msg.RawOctets = "", nil // stored, so no longer held
			for i := range rows {
				rows[i].MessageID = msg.ID
			}
			if len(rows) != 0 {
				if err := tx.Create(&rows).Error; err != nil {
					return err
				}
			}
			usage := tx.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", accountID).Updates(map[string]any{
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
			result = apitypes.ImportMessageResult{ID: msg.ID, Imported: true, MessageID: msg.MsgID, Folder: msg.Folder}
		}
		entry := middleware.NewAuditEntry("messages.import", r, time.Now())
		entry.Status = http.StatusOK
		entry.Target["account_id"] = strconv.FormatUint(uint64(accountID), 10)
		entry.Target["message_id"] = strconv.FormatUint(uint64(result.ID), 10)
		entry.Target["imported"] = strconv.FormatBool(result.Imported)
		return audit.Append(tx, entry)
	})
	if err != nil {
		return apitypes.ImportMessageResult{}, fmt.Errorf("import historical message: %w", err)
	}
	// An open client learns of the message; a phone is not told, since this is
	// mail the account already has.
	if result.Imported && h.broker != nil {
		h.broker.Publish(uint(accountID), SSEEvent{Type: "new_message", Data: map[string]interface{}{
			"message_id": result.ID, "folder": result.Folder, "sender": msg.Sender, "subject": msg.Subject,
		}})
	}
	return result, nil
}

// importedText is a decoded header or body as a text column can hold it.
func importedText(value string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(value, "\x00", "�"), "�")
}

// storeAttachmentPart stores one decoded part as it is read. The object store has to be told a
// size and a checksum before the upload, so a part bound for it goes through a file first.
func (h *MessageImportHandler) storeAttachmentPart(ctx context.Context, part io.Reader) (rmime.StoredPart, error) {
	if h.store == nil {
		ref, checksum, size, err := attachmentstore.PutFilesystemReader(ctx, h.directory, part)
		return rmime.StoredPart{Storage: "filesystem", Ref: ref, Checksum: checksum, Size: size}, err
	}
	file, done, err := newImportSpool()
	if err != nil {
		return rmime.StoredPart{}, err
	}
	defer done()
	size, err := io.Copy(file, part)
	if err != nil {
		return rmime.StoredPart{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return rmime.StoredPart{}, err
	}
	ref, checksum, err := h.store.PutReader(ctx, file, size)
	return rmime.StoredPart{Storage: "s3", Ref: ref, Checksum: checksum, Size: size}, err
}

// storedImportSource is a stored message with what identifies its source, which the database
// works out so that the source itself is not read back here.
type storedImportSource struct {
	ID           uint
	MsgID        string `gorm:"column:message_id"`
	Folder       string
	SourceSHA256 []byte `gorm:"column:source_sha256"`
	SourceOctets int64  `gorm:"column:source_octets"`
}

// storedImportSourceColumns reads a message's source as models.Message.Source does: its octets
// where it has them, its text otherwise.
const storedImportSourceColumns = `id, message_id, folder,
	sha256(coalesce(nullif(raw_octets, ''::bytea), convert_to(coalesce(raw_message, ''), 'UTF8'))) AS source_sha256,
	octet_length(coalesce(nullif(raw_octets, ''::bytea), convert_to(coalesce(raw_message, ''), 'UTF8'))) AS source_octets`

// sameContent is the first of messages holding exactly the source with this checksum. One with
// no stored source cannot be compared, so it counts as the same: a rerun must not copy it.
func sameContent(messages []storedImportSource, checksum [sha256.Size]byte) *storedImportSource {
	for i := range messages {
		if messages[i].SourceOctets == 0 || bytes.Equal(messages[i].SourceSHA256, checksum[:]) {
			return &messages[i]
		}
	}
	return nil
}
