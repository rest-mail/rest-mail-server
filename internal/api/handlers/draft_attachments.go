package handlers

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type draftAttachmentUpload struct {
	ID          uint32 `path:"id" minimum:"1"`
	Filename    string `header:"X-Attachment-Filename,required" minLength:"1" maxLength:"255" description:"UTF-8 display filename, not a filesystem path. CR, LF and NUL are refused."`
	ContentType string `header:"X-Attachment-Content-Type,required" minLength:"1" maxLength:"255" description:"The attachment's MIME media type, for example application/pdf."`
}

type draftAttachmentBody struct {
	ID          uint32 `path:"id" minimum:"1"`
	Filename    string `header:"X-Attachment-Filename,required" minLength:"1" maxLength:"255" description:"UTF-8 display filename, not a filesystem path. CR, LF and NUL are refused."`
	ContentType string `header:"X-Attachment-Content-Type,required" minLength:"1" maxLength:"255" description:"The attachment's MIME media type, for example application/pdf."`
	Body        string `body:"application/octet-stream" format:"binary"`
}

type draftAttachmentPath struct {
	ID           uint32 `path:"id" minimum:"1"`
	AttachmentID uint32 `path:"attachmentId" minimum:"1"`
}

var draftAttachmentUploadPath = regexp.MustCompile(`^/api/v1/messages/draft/[^/]+/attachments/?$`)

func DraftAttachmentUploadRoute(r *http.Request) bool {
	return r.Method == http.MethodPost && draftAttachmentUploadPath.MatchString(r.URL.Path)
}

func (h *MessageHandler) registerDraftAttachments(rs endpoint.Routes) {
	security := handlerChecked(models.CapMailWrite, folderSchemes...)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/messages/draft/{id}/attachments",
		OperationID: "uploadDraftAttachment", Summary: "Upload an attachment to a draft", Tags: []string{"Drafts", "Attachments"}, Status: http.StatusCreated,
		Description:      "Upload raw octets with X-Attachment-Filename and X-Attachment-Content-Type. Streams through a bounded temporary file into the configured filesystem or S3-compatible attachment store. The general JSON API body cap does not apply; SMTP_MAX_MESSAGE_SIZE bounds this upload and the draft's combined body and attachment bytes (413 message_too_large); final encoded MIME is checked again on send. MIME_MAX_PARTS also bounds attachments plus body parts, including empty uploads (413 message_too_large). Account and owner storage quotas apply (422 mailbox_full). The draft must be writable; a missing, non-draft, deleted or inaccessible message is 404. JSON attachments fields remain unsupported: upload here, then sendDraft uses stored uploads. Empty files are supported.",
		DescribedRequest: draftAttachmentBody{}, Responses: []oapifly.EndpointResponse{{Status: http.StatusUnprocessableEntity,
			Description: "Binding validation or draft-account quota refusal", Type: oapifly.TypeOf[ForwardRefusalResponse]()}},
		Errors: []int{400, 401, 403, 404, 413, 422}, Security: security}, h.uploadDraftAttachment)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/messages/draft/{id}/attachments",
		OperationID: "listDraftAttachments", Summary: "List a draft's attachments", Tags: []string{"Drafts", "Attachments"},
		Description: "Lists uploads in ID order. Requires mail:write on this draft; missing or inaccessible drafts are 404.",
		Errors:      []int{401, 403, 404}, Security: security}, h.listDraftAttachments)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/messages/draft/{id}/attachments/{attachmentId}",
		OperationID: "deleteDraftAttachment", Summary: "Remove an attachment from a draft", Tags: []string{"Drafts", "Attachments"}, Status: http.StatusNoContent,
		Description: "Removes only an upload belonging to this writable draft and releases its quota. Missing, foreign or already-sent drafts and attachments are 404. Unreferenced stored objects are reclaimed by the existing attachment cleaner after its grace period.",
		Errors:      []int{401, 403, 404}, Security: security}, h.deleteDraftAttachment)
}

func (h *MessageHandler) writableDraft(r *http.Request, id uint32, lock bool) (*models.Message, error) {
	if middleware.GetClaims(r) == nil {
		return nil, errNotSignedIn
	}
	query := h.db.WithContext(r.Context())
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var draft models.Message
	if err := query.Where("id = ? AND is_draft = ? AND is_deleted = ?", id, true, false).First(&draft).Error; err != nil {
		return nil, draftLookupRefusal(err)
	}
	if err := h.draftAccess(r, &draft); err != nil {
		return nil, err
	}
	return &draft, nil
}

func (h *MessageHandler) uploadDraftAttachment(r *http.Request, in draftAttachmentUpload) (MessageAttachment, error) {
	var answer MessageAttachment
	if !utf8.ValidString(in.Filename) || strings.ContainsAny(in.Filename, "\x00\r\n") || strings.TrimSpace(in.Filename) == "" || len(in.Filename) > 255 {
		return answer, endpoint.Invalid(map[string]string{"X-Attachment-Filename": "a UTF-8 filename of 1 to 255 bytes without CR, LF or NUL is required"})
	}
	media, _, err := mime.ParseMediaType(in.ContentType)
	if err != nil || !strings.Contains(media, "/") || strings.ContainsAny(in.ContentType, "\x00\r\n") || len(in.ContentType) > 255 {
		return answer, endpoint.Invalid(map[string]string{"X-Attachment-Content-Type": "a MIME media type of at most 255 bytes is required"})
	}
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		scoped := *h
		scoped.db = tx
		draft, err := scoped.writableDraft(r, in.ID, true)
		if err != nil {
			return err
		}
		var stored struct {
			Bytes int64
			Count int64
		}
		if err := tx.Model(&models.Attachment{}).Where("message_id = ?", draft.ID).Select("COALESCE(SUM(size_bytes), 0) AS bytes, COUNT(*) AS count").Scan(&stored).Error; err != nil {
			return err
		}
		bodyParts := int64(1)
		if draft.BodyText != "" && draft.BodyHTML != "" {
			bodyParts = 3
		}
		if stored.Count+1+bodyParts > int64(rmime.CurrentLimits().MaxParts) {
			return errDraftAttachmentTooLarge
		}
		remaining := h.maxForwardBytes - int64(len(draft.Subject)+len(draft.BodyText)+len(draft.BodyHTML)) - stored.Bytes
		if remaining < 0 || r.ContentLength > remaining {
			return errDraftAttachmentTooLarge
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(nil, r.Body, remaining)
		}
		file, done, err := endpoint.SpoolBody(r, "application/octet-stream")
		if err != nil {
			var refusal *endpoint.Error
			if errors.As(err, &refusal) && refusal.Status == http.StatusRequestEntityTooLarge {
				return errDraftAttachmentTooLarge
			}
			return err
		}
		defer done()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if err := repositories.ReserveStorage(tx, draft.MailboxID, info.Size()); err != nil {
			if errors.Is(err, repositories.ErrStorageQuota) {
				return endpoint.Refuse(422, "mailbox_full", "The draft account has insufficient quota")
			}
			return err
		}
		row := models.Attachment{MessageID: draft.ID, Filename: in.Filename, ContentType: in.ContentType, SizeBytes: info.Size(), StorageType: "filesystem"}
		if h.forwardStore != nil {
			row.StorageType = "s3"
			row.StorageRef, row.Checksum, err = h.forwardStore.PutReader(r.Context(), file, info.Size())
		} else {
			row.StorageRef, row.Checksum, _, err = attachmentstore.PutFilesystemReader(r.Context(), h.forwardRoot, file)
		}
		if err != nil {
			return fmt.Errorf("store draft attachment: %w", err)
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := chargeDraftAttachmentUsage(tx, draft.MailboxID, info.Size()); err != nil {
			return err
		}
		if err := tx.Model(draft).Update("has_attachments", true).Error; err != nil {
			return err
		}
		answer = attachmentOf(row)
		return nil
	})
	return answer, err
}

var errDraftAttachmentTooLarge = endpoint.Refuse(http.StatusRequestEntityTooLarge, "message_too_large", "The draft exceeds the message size limit")

func chargeDraftAttachmentUsage(tx *gorm.DB, mailboxID uint, size int64) error {
	usage := models.QuotaUsage{MailboxID: mailboxID, AttachmentBytes: size}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "mailbox_id"}}, DoUpdates: clause.Assignments(map[string]any{
		"attachment_bytes": gorm.Expr("GREATEST(quota_usage.attachment_bytes + ?, 0)", size),
	})}).Create(&usage).Error
}

func (h *MessageHandler) listDraftAttachments(r *http.Request, in messagePath) (endpoint.List[MessageAttachment], error) {
	h = h.forRequest(r)
	if _, err := h.writableDraft(r, in.ID, false); err != nil {
		return endpoint.List[MessageAttachment]{}, err
	}
	var rows []models.Attachment
	if err := h.db.Where("message_id = ?", in.ID).Order("id").Find(&rows).Error; err != nil {
		return endpoint.List[MessageAttachment]{}, err
	}
	answer := make([]MessageAttachment, len(rows))
	for index, row := range rows {
		answer[index] = attachmentOf(row)
	}
	return endpoint.List[MessageAttachment]{Data: answer}, nil
}

func (h *MessageHandler) deleteDraftAttachment(r *http.Request, in draftAttachmentPath) (struct{}, error) {
	err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		scoped := *h
		scoped.db = tx
		draft, err := scoped.writableDraft(r, in.ID, true)
		if err != nil {
			return err
		}
		var row models.Attachment
		if err := tx.Where("id = ? AND message_id = ?", in.AttachmentID, draft.ID).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return endpoint.Refuse(404, "not_found", "Attachment not found")
			}
			return err
		}
		if err := releaseDraftAttachmentQuota(tx, draft.MailboxID, row.SizeBytes); err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		var remaining int64
		if err := tx.Model(&models.Attachment{}).Where("message_id = ?", draft.ID).Count(&remaining).Error; err != nil {
			return err
		}
		return tx.Model(draft).Update("has_attachments", remaining > 0).Error
	})
	return struct{}{}, err
}

func releaseDraftAttachmentQuota(tx *gorm.DB, mailboxID uint, size int64) error {
	if _, err := repositories.StorageQuotaChain(tx, mailboxID, true); err != nil {
		return err
	}
	if err := tx.Model(&models.Mailbox{}).Where("id = ?", mailboxID).Update("quota_used_bytes", gorm.Expr("GREATEST(quota_used_bytes - ?, 0)", size)).Error; err != nil {
		return err
	}
	return chargeDraftAttachmentUsage(tx, mailboxID, -size)
}
