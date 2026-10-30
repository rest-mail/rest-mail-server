package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	rmail "github.com/restmail/restmail/internal/mail"
	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
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
		legacy := row.StorageType == "s3" && strings.HasPrefix(row.StorageRef, h.forwardRoot+"/")
		if row.StorageType == "filesystem" || legacy {
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
	raw, err := buildOutboundRaw(outboundMessage{Sender: req.Sender, FromName: sender.DisplayName, From: req.From, To: req.To, Cc: req.Cc, Subject: req.Subject, BodyText: req.BodyText, BodyHTML: req.BodyHTML, MessageID: sent.MsgID, InReplyTo: req.InReplyTo, Date: sent.ReceivedAt, Extra: withListUnsubscribe(extra, req.ListUnsubscribe), Attachments: parts})
	if err != nil {
		return zero, endpoint.Refuse(500, "internal_error", "Failed to build the forwarded message")
	}
	unsigned := raw
	raw, err = h.signOutboundDKIM(rmail.DomainFromAddress(req.From), raw)
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
	toJSON, _ := json.Marshal(req.To)
	ccJSON, _ := json.Marshal(req.Cc)
	if req.Cc == nil {
		ccJSON = []byte("[]")
	}
	recipients := append(append(append([]string{}, req.To...), req.Cc...), req.Bcc...)
	var traces forwardTraces
	var delivered []models.Message
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		cost := int64(sent.SizeBytes) + attachmentBytes
		usage := tx.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", sender.ID).Updates(map[string]any{"subject_bytes": gorm.Expr("subject_bytes + ?", len(sent.Subject)), "body_bytes": gorm.Expr("body_bytes + ?", len(sent.BodyText)+len(sent.BodyHTML)), "attachment_bytes": gorm.Expr("attachment_bytes + ?", attachmentBytes), "message_count": gorm.Expr("message_count + 1")})
		if usage.Error != nil {
			return usage.Error
		}
		if usage.RowsAffected != 1 {
			return errors.New("sender quota usage missing")
		}
		quota := tx.Model(&models.Mailbox{}).Where("id = ? AND (quota_bytes <= 0 OR quota_used_bytes + ? <= quota_bytes)", sender.ID, cost).Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", cost))
		if quota.Error != nil {
			return quota.Error
		}
		if quota.RowsAffected != 1 {
			return endpoint.Refuse(422, "mailbox_full", "The sender account has insufficient quota")
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
		for _, rcpt := range recipients {
			var box models.Mailbox
			err := repositories.FindActiveMailboxByAddress(tx, rcpt, &box)
			if err == nil {
				local := *h
				local.db = tx
				local.broker = nil
				local.newMail = nil
				local.recorder = &traces
				msg, err := local.deliverToLocal(r.Context(), localDeliveryParams{Mailbox: box, Sender: req.From, SenderName: sender.DisplayName, RecipientsTo: toJSON, RecipientsCc: ccJSON, Subject: req.Subject, BodyText: req.BodyText, BodyHTML: req.BodyHTML, MessageID: sent.MsgID, InReplyTo: req.InReplyTo, RawMessage: raw, RequireTLS: req.RequireTLS, IndependentAttachments: true})
				if err != nil {
					return err
				}
				if msg != nil {
					delivered = append(delivered, *msg)
				}
			} else if errors.Is(err, gorm.ErrRecordNotFound) {
				id := sent.ID
				if err := tx.Create(&models.OutboundQueue{MessageID: &id, Sender: req.From, Recipient: rcpt, Domain: rmail.DomainFromAddress(rcpt), RawMessage: raw, Status: "pending", RequireTLS: req.RequireTLS}).Error; err != nil {
					return err
				}
			} else {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return zero, fmt.Errorf("commit forwarded message: %w", err)
	}
	for _, trace := range traces.rows {
		h.recordTrace(trace)
	}
	if queued != nil {
		queued.Outcome = outcomeQueued
		queued.RFCMessageID = sent.MsgID
		id := sent.ID
		queued.MessageID = &id
		h.recordTrace(buildTrace(*queued))
	}
	if h.broker != nil {
		h.broker.Publish(sender.ID, SSEEvent{Type: "message_sent", Data: map[string]any{"message_id": sent.ID, "folder": "Sent", "subject": sent.Subject}})
	}
	for _, msg := range delivered {
		if h.broker != nil {
			h.broker.Publish(msg.MailboxID, SSEEvent{Type: "new_message", Data: map[string]any{"message_id": msg.ID, "folder": msg.Folder, "subject": msg.Subject, "sender": msg.Sender}})
			var unread int64
			if err := h.db.WithContext(r.Context()).Model(&models.Message{}).Where("mailbox_id = ? AND folder = ? AND is_read = ? AND is_deleted = ?", msg.MailboxID, "INBOX", false, false).Count(&unread).Error; err == nil {
				h.broker.Publish(msg.MailboxID, SSEEvent{Type: "folder_update", Data: map[string]any{"folder": "INBOX", "unread_count": unread}})
			}
		}
		if h.newMail != nil && msg.Folder == "INBOX" && !msg.IsRead {
			h.newMail.NewMail(msg.MailboxID)
		}
	}
	for _, rcpt := range recipients {
		contact := models.Contact{MailboxID: sender.ID, Email: rcpt}
		h.db.WithContext(r.Context()).Where(contact).Attrs(models.Contact{TrustLevel: "auto", Source: "sent"}).FirstOrCreate(&models.Contact{})
	}
	return endpoint.Status[any]{Code: http.StatusCreated, Value: messageOf(*sent)}, nil
}

type forwardTraces struct{ rows []models.MessageTrace }

func (b *forwardTraces) Record(trace models.MessageTrace) { b.rows = append(b.rows, trace) }

// Local forwarding uses the recipient's inbound pipeline, then gives every
// surviving part an independent object. An unextracted zero-byte MIME part is
// still a part; it must acquire a stored reference rather than disappear.
func (h *MessageHandler) copyLocalForwardParts(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.EmailJSON, error) {
	copy := *email
	remaining := h.maxForwardBytes
	clone := func(input []pipeline.Attachment) ([]pipeline.Attachment, error) {
		output := make([]pipeline.Attachment, 0, len(input))
		for _, part := range input {
			data, err := h.forwardPartBytes(ctx, part, remaining)
			if err != nil {
				return nil, err
			}
			remaining -= int64(len(data))
			if h.forwardStore != nil {
				part.Ref, part.Checksum, err = h.forwardStore.PutIndependent(ctx, data)
				part.Storage = "s3"
			} else {
				part.Ref, part.Checksum, err = attachmentstore.PutIndependentFilesystem(ctx, h.forwardRoot, data)
				part.Storage = "filesystem"
			}
			if err != nil {
				return nil, err
			}
			part.Content = ""
			output = append(output, part)
		}
		return output, nil
	}
	var err error
	copy.Attachments, err = clone(email.Attachments)
	if err != nil {
		return nil, err
	}
	copy.Inline, err = clone(email.Inline)
	if err != nil {
		return nil, err
	}
	return &copy, nil
}

func (h *MessageHandler) forwardPartBytes(ctx context.Context, part pipeline.Attachment, remaining int64) ([]byte, error) {
	var data []byte
	var err error
	if part.Ref == "" {
		data, err = base64.StdEncoding.DecodeString(part.Content)
	} else {
		var reader io.ReadCloser
		if part.Storage == "s3" && h.forwardStore != nil && !strings.HasPrefix(part.Ref, h.forwardRoot+"/") {
			reader, err = h.forwardStore.Open(ctx, part.Ref)
		} else if part.Storage == "filesystem" || (part.Storage == "s3" && strings.HasPrefix(part.Ref, h.forwardRoot+"/")) {
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
