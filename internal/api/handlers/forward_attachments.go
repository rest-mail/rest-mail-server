package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

func (h *MessageHandler) SetForwardStorage(maxBytes int64, store *attachmentstore.S3) {
	if maxBytes <= 0 {
		maxBytes = config.DefaultSMTPMaxMessageSize
	}
	h.maxForwardBytes, h.forwardStore = maxBytes, store
}

var errForwardTooLarge = endpoint.Refuse(http.StatusRequestEntityTooLarge, "message_too_large", "The forwarded message exceeds the message size limit")
var errForwardAttachment = endpoint.Refuse(http.StatusInternalServerError, "attachment_unavailable", "Could not read an original attachment")

func (h *MessageHandler) readForwardParts(r *http.Request, original *models.Message) ([]pipeline.Attachment, error) {
	var rows []models.Attachment
	if err := h.db.WithContext(r.Context()).Where("message_id = ?", original.ID).Order("id").Find(&rows).Error; err != nil {
		return nil, errForwardAttachment
	}
	if original.HasAttachments && len(rows) == 0 {
		return nil, errForwardAttachment
	}
	// Storage holds the bytes. The original MIME source supplies inline Content-ID
	// metadata, which old attachment rows do not carry. Never forward stale source
	// content instead of reading and checking the stored object.
	var metadata []pipeline.Attachment
	if parsed, err := rmime.Parse(original.Source()); err == nil {
		metadata = append(parsed.Inline, parsed.Attachments...)
	}
	parts := make([]pipeline.Attachment, 0, len(rows))
	remaining := h.maxForwardBytes
	for _, row := range rows {
		if row.SizeBytes < 0 || row.SizeBytes > remaining {
			return nil, errForwardTooLarge
		}
		var reader io.ReadCloser
		if row.StorageType == "filesystem" {
			path, err := attachmentstore.ResolveFilesystemPath(h.forwardRoot, row.StorageRef)
			if err != nil {
				return nil, errForwardAttachment
			}
			reader, err = os.Open(path)
			if err != nil {
				return nil, errForwardAttachment
			}
		} else if row.StorageType == "s3" && h.forwardStore != nil {
			file, err := h.forwardStore.Open(r.Context(), row.StorageRef)
			if err != nil {
				return nil, errForwardAttachment
			}
			reader = file
		} else {
			return nil, errForwardAttachment
		}
		data, err := io.ReadAll(io.LimitReader(reader, remaining+1))
		closeErr := reader.Close()
		if err != nil || closeErr != nil || r.Context().Err() != nil {
			return nil, errForwardAttachment
		}
		if int64(len(data)) > remaining {
			return nil, errForwardTooLarge
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(data))
		if int64(len(data)) != row.SizeBytes || (row.Checksum != "" && checksum != row.Checksum) {
			return nil, errForwardAttachment
		}
		remaining -= int64(len(data))
		part := pipeline.Attachment{Filename: row.Filename, ContentType: row.ContentType, Size: int64(len(data)), Disposition: "attachment", Content: base64.StdEncoding.EncodeToString(data), Checksum: checksum}
		for i, candidate := range metadata {
			decoded, err := base64.StdEncoding.DecodeString(candidate.Content)
			if err == nil && candidate.Filename == row.Filename && bytes.Equal(decoded, data) {
				part.Disposition, part.ContentID = candidate.Disposition, candidate.ContentID
				metadata = append(metadata[:i], metadata[i+1:]...)
				break
			}
		}
		parts = append(parts, part)
	}
	// Older extraction skipped zero-byte parts. Their complete content is
	// already represented by the stored MIME source; preserve those too.
	for _, part := range metadata {
		if part.Size == 0 && part.Content == "" {
			parts = append(parts, part)
		}
	}
	return parts, nil
}

// The normal send validation, sender policy, rate limit and outbound pipeline
// have already run. Commit the forward's Sent copy, independent attachment
// references, quota and recipient work together; refuse any partial outcome.
func (h *MessageHandler) commitForward(r *http.Request, req sendMessageRequest, sender *models.Mailbox, sent *models.Message, parts []pipeline.Attachment, extra map[string]string, queued *traceInputs) (endpoint.Status[any], error) {
	var zero endpoint.Status[any]
	// Outbound transforms may replace content with a stored reference. Read the
	// transformed parts with the same bounds and integrity checks before MIME,
	// into a copy: the caller's parts stay as they were passed.
	parts = append([]pipeline.Attachment(nil), parts...)
	remaining := h.maxForwardBytes
	for i, part := range parts {
		data, err := h.forwardPartBytes(r.Context(), part, remaining)
		if err != nil {
			return zero, err
		}
		remaining -= int64(len(data))
		parts[i].Content = base64.StdEncoding.EncodeToString(data)
		parts[i].Size = int64(len(data))
	}
	raw, err := buildOutboundRaw(outboundMessage{Sender: req.Sender, FromName: sender.DisplayName, From: req.From, To: req.To, Cc: req.Cc, Subject: req.Subject, BodyText: req.BodyText, BodyHTML: req.BodyHTML, MessageID: sent.MsgID, InReplyTo: req.InReplyTo, References: sent.References, Date: sent.ReceivedAt, Extra: withListUnsubscribe(extra, req.ListUnsubscribe), Attachments: parts})
	if err != nil {
		return zero, endpoint.Refuse(500, "internal_error", "Failed to build the forwarded message")
	}
	unsigned := raw
	raw, err = h.signOutboundDKIM(r.Context(), rmail.DomainFromAddress(req.From), raw)
	if err != nil {
		return zero, endpoint.Refuse(503, "deferred", "Try again later")
	}
	// As for a send: a one-click claim must not leave unsigned (#144).
	if req.ListUnsubscribe != nil && req.ListUnsubscribe.HTTPS != "" && raw == unsigned {
		return zero, endpoint.Refuse(503, "deferred", "Try again later")
	}
	if int64(len(raw)) > h.maxForwardBytes {
		return zero, errForwardTooLarge
	}
	sent.RawMessage, sent.RawSize, sent.HasAttachments = raw, len(raw), len(parts) > 0
	var attachmentBytes int64
	for _, part := range parts {
		attachmentBytes += part.Size
	}
	recipients := append(append(append([]string{}, req.To...), req.Cc...), req.Bcc...)
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		cost := int64(sent.SizeBytes) + attachmentBytes
		if err := repositories.ReserveStorage(tx, sender.ID, cost); err != nil {
			if errors.Is(err, repositories.ErrStorageQuota) {
				return endpoint.Refuse(422, "mailbox_full", "The sender account has insufficient quota")
			}
			return err
		}
		usage := tx.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", sender.ID).Updates(map[string]any{"subject_bytes": gorm.Expr("subject_bytes + ?", len(sent.Subject)), "body_bytes": gorm.Expr("body_bytes + ?", len(sent.BodyText)+len(sent.BodyHTML)), "attachment_bytes": gorm.Expr("attachment_bytes + ?", attachmentBytes), "message_count": gorm.Expr("message_count + 1")})
		if usage.Error != nil {
			return usage.Error
		}
		if usage.RowsAffected != 1 {
			return errors.New("sender quota usage missing")
		}
		if err := tx.Create(sent).Error; err != nil {
			return err
		}
		for _, part := range parts {
			data, err := base64.StdEncoding.DecodeString(part.Content)
			if err != nil {
				return err
			}
			var ref, checksum, storage string
			if h.forwardStore != nil {
				ref, checksum, err = h.forwardStore.PutIndependent(r.Context(), data)
				storage = "s3"
			} else {
				ref, checksum, err = attachmentstore.PutIndependentFilesystem(r.Context(), h.forwardRoot, data)
				storage = "filesystem"
			}
			if err != nil {
				return err
			}
			row := models.Attachment{MessageID: sent.ID, Filename: part.Filename, ContentType: part.ContentType, SizeBytes: int64(len(data)), StorageType: storage, StorageRef: ref, Checksum: checksum}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		// Every recipient goes on the outbound queue, one on this server as much
		// as one elsewhere (#1564): the worker delivers it through the normal route.
		rows := queueRowsFor(recipients, sent.ID, req.From, raw, req.RequireTLS)
		return tx.Create(&rows).Error
	})
	if err != nil {
		return zero, fmt.Errorf("commit forwarded message: %w", err)
	}
	if queued != nil {
		queued.Outcome = outcomeQueued
		queued.RFCMessageID = sent.MsgID
		id := sent.ID
		queued.MessageID = &id
		h.recordTrace(r.Context(), buildTrace(*queued))
	}
	if h.broker != nil {
		h.broker.Publish(r.Context(), sender.ID, SSEEvent{Type: "message_sent", Data: map[string]any{"message_id": sent.ID, "folder": "Sent", "subject": sent.Subject}})
	}
	for _, rcpt := range recipients {
		contact := models.Contact{MailboxID: sender.ID, Email: rcpt}
		h.db.WithContext(r.Context()).Where(contact).Attrs(models.Contact{TrustLevel: "auto", Source: "sent"}).FirstOrCreate(&models.Contact{})
	}
	return endpoint.Status[any]{Code: http.StatusCreated, Value: messageOf(*sent)}, nil
}

func (h *MessageHandler) forwardPartBytes(ctx context.Context, part pipeline.Attachment, remaining int64) ([]byte, error) {
	var data []byte
	var err error
	if part.Ref == "" {
		data, err = base64.StdEncoding.DecodeString(part.Content)
	} else {
		var reader io.ReadCloser
		if part.Storage == "s3" && h.forwardStore != nil {
			reader, err = h.forwardStore.Open(ctx, part.Ref)
		} else if part.Storage == "filesystem" {
			var path string
			path, err = attachmentstore.ResolveFilesystemPath(h.forwardRoot, part.Ref)
			if err == nil {
				reader, err = os.Open(path)
			}
		} else {
			err = errForwardAttachment
		}
		if err != nil {
			return nil, errForwardAttachment
		}
		data, err = io.ReadAll(io.LimitReader(reader, remaining+1))
		closeErr := reader.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil || ctx.Err() != nil {
		return nil, errForwardAttachment
	}
	if int64(len(data)) > remaining {
		return nil, errForwardTooLarge
	}
	if int64(len(data)) != part.Size || (part.Checksum != "" && fmt.Sprintf("%x", sha256.Sum256(data)) != part.Checksum) {
		return nil, errForwardAttachment
	}
	return data, nil
}
