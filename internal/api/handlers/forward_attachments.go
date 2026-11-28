package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
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

// readForwardParts is the parts of a message to be sent on, from their rows alone: each part
// is carried by reference to its stored object, which is read through once here to check it is
// whole and unchanged, and is never held in memory. The stored message is not read for them;
// its rows keep each part's disposition and Content-ID, and its empty parts (#2107).
func (h *MessageHandler) readForwardParts(r *http.Request, original *models.Message) ([]pipeline.Attachment, error) {
	var rows []models.Attachment
	if err := h.db.WithContext(r.Context()).Select("id", "filename", "content_type", "size_bytes", "storage_type", "storage_ref", "checksum", "disposition", "content_id").
		Where("message_id = ?", original.ID).Order("id").Find(&rows).Error; err != nil {
		return nil, errForwardAttachment
	}
	if original.HasAttachments && len(rows) == 0 {
		return nil, errForwardAttachment
	}
	parts := make([]pipeline.Attachment, 0, len(rows))
	remaining := h.maxForwardBytes
	for _, row := range rows {
		if row.SizeBytes < 0 || row.SizeBytes > remaining {
			return nil, errForwardTooLarge
		}
		part := pipeline.Attachment{Filename: row.Filename, ContentType: row.ContentType, Size: row.SizeBytes,
			Disposition: models.PartDisposition(row.Disposition), ContentID: row.ContentID,
			Storage: row.StorageType, Ref: row.StorageRef, Checksum: row.Checksum}
		checksum, err := h.checkForwardPart(r.Context(), part)
		if err != nil {
			return nil, err
		}
		part.Checksum = checksum
		remaining -= row.SizeBytes
		parts = append(parts, part)
	}
	return parts, nil
}

// openForwardPart opens a part's bytes: the stored object it refers to, or else its content.
func (h *MessageHandler) openForwardPart(ctx context.Context, part pipeline.Attachment) (io.ReadCloser, error) {
	if part.Ref == "" {
		return io.NopCloser(base64.NewDecoder(base64.StdEncoding, strings.NewReader(part.Content))), nil
	}
	switch {
	case part.Storage == "s3" && h.forwardStore != nil:
		reader, err := h.forwardStore.Open(ctx, part.Ref)
		if err != nil {
			return nil, errForwardAttachment
		}
		return reader, nil
	case part.Storage == "filesystem":
		path, err := attachmentstore.ResolveFilesystemPath(h.forwardRoot, part.Ref)
		if err != nil {
			return nil, errForwardAttachment
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, errForwardAttachment
		}
		return file, nil
	}
	return nil, errForwardAttachment
}

// openCheckedPart opens a part's bytes so that reading them fails, at the end at the latest,
// unless they are exactly its size and, when it has one, its checksum.
func (h *MessageHandler) openCheckedPart(ctx context.Context, part pipeline.Attachment) (*checkedPart, error) {
	if part.Size < 0 {
		return nil, errForwardAttachment
	}
	reader, err := h.openForwardPart(ctx, part)
	if err != nil {
		return nil, err
	}
	return &checkedPart{ctx: ctx, r: reader, size: part.Size, checksum: part.Checksum, hash: sha256.New()}, nil
}

// checkForwardPart reads a part through, through a fixed buffer, and answers its checksum once
// it is shown to be whole and unchanged.
func (h *MessageHandler) checkForwardPart(ctx context.Context, part pipeline.Attachment) (string, error) {
	reader, err := h.openCheckedPart(ctx, part)
	if err != nil {
		return "", err
	}
	_, err = attachmentstore.Copy(io.Discard, reader)
	if closeErr := reader.Close(); err == nil && closeErr != nil {
		err = errForwardAttachment
	}
	if err != nil {
		return "", err
	}
	return reader.sum, nil
}

// checkedPart reads exactly a part's octets and fails, rather than ending, when there are more
// or fewer of them, or they are not the part's checksum.
type checkedPart struct {
	ctx      context.Context
	r        io.ReadCloser
	size     int64
	read     int64
	checksum string
	hash     hash.Hash
	sum      string
}

func (c *checkedPart) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, errForwardAttachment
	}
	if c.read == c.size {
		// One octet more than the part's size is a part that changed.
		var extra [1]byte
		if n, err := io.ReadFull(c.r, extra[:]); n != 0 || (err != nil && err != io.EOF) {
			return 0, errForwardAttachment
		}
		sum := hex.EncodeToString(c.hash.Sum(nil))
		if c.checksum != "" && sum != c.checksum {
			return 0, errForwardAttachment
		}
		c.sum = sum
		return 0, io.EOF
	}
	if left := c.size - c.read; int64(len(p)) > left {
		p = p[:left]
	}
	n, err := c.r.Read(p)
	c.read += int64(n)
	c.hash.Write(p[:n])
	if err == io.EOF {
		if c.read != c.size {
			return n, errForwardAttachment
		}
		err = nil
	}
	if err != nil {
		return n, errForwardAttachment
	}
	return n, nil
}

func (c *checkedPart) Close() error { return c.r.Close() }

// referencedParts lets the outbound pipeline's filters read the parts a send hands it by
// reference, and no other: a reference a filter made up is not opened (#2107).
func (h *MessageHandler) referencedParts(parts []pipeline.Attachment) pipeline.AttachmentOpener {
	handed := handedParts{h: h, parts: make(map[[2]string]pipeline.Attachment, len(parts))}
	for _, part := range parts {
		if part.Ref != "" {
			handed.parts[[2]string{part.Storage, part.Ref}] = part
		}
	}
	return handed
}

type handedParts struct {
	h     *MessageHandler
	parts map[[2]string]pipeline.Attachment
}

func (p handedParts) Holds(att pipeline.Attachment) bool {
	part, ok := p.parts[[2]string{att.Storage, att.Ref}]
	return ok && part.Size == att.Size
}

func (p handedParts) Open(ctx context.Context, att pipeline.Attachment) (io.ReadCloser, error) {
	if !p.Holds(att) {
		return nil, errForwardAttachment
	}
	reader, err := p.h.openCheckedPart(ctx, p.parts[[2]string{att.Storage, att.Ref}])
	if err != nil {
		return nil, err
	}
	return reader, nil
}

// putIndependentPart stores a copy of a part's bytes that no other message shares, streaming
// them from where the part is, and checking them on the way.
func (h *MessageHandler) putIndependentPart(ctx context.Context, part pipeline.Attachment) (storage, ref, checksum string, err error) {
	source, err := h.openCheckedPart(ctx, part)
	if err != nil {
		return "", "", "", err
	}
	if h.forwardStore == nil {
		ref, checksum, _, err := attachmentstore.PutIndependentFilesystemReader(ctx, h.forwardRoot, source)
		return "filesystem", ref, checksum, errors.Join(err, source.Close())
	}

	// The S3 uploader seeks for signing and retries. Spool the verified GET
	// before the PUT so the upload does not depend on an open download.
	file, closeSpool, err := h.openOutboundSpool("restmail-sent-part-*")
	if err != nil {
		_ = source.Close()
		return "", "", "", err
	}
	defer closeSpool()
	_, err = attachmentstore.Copy(file, source)
	if err := errors.Join(err, source.Close()); err != nil {
		return "", "", "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", "", "", err
	}
	ref, checksum, err = h.forwardStore.PutIndependentReader(ctx, file, part.Size)
	return "s3", ref, checksum, err
}

// prepareComposedParts pins a checksum for any part supplied by a transform
// so the rendered source and the independent Sent objects contain the same bytes.
func (h *MessageHandler) prepareComposedParts(ctx context.Context, parts []pipeline.Attachment) (int64, error) {
	var size int64
	for i, part := range parts {
		if part.Size < 0 || part.Size > h.maxForwardBytes-size {
			return 0, errForwardTooLarge
		}
		if part.Checksum == "" {
			checksum, err := h.checkForwardPart(ctx, part)
			if err != nil {
				return 0, err
			}
			parts[i].Checksum = checksum
		}
		size += part.Size
	}
	return size, nil
}

func reserveForwardStorage(tx *gorm.DB, sent *models.Message, attachmentBytes int64) error {
	cost := int64(sent.SizeBytes) + attachmentBytes
	if err := repositories.ReserveStorage(tx, sent.MailboxID, cost); err != nil {
		if errors.Is(err, repositories.ErrStorageQuota) {
			return endpoint.Refuse(422, "mailbox_full", "The sender account has insufficient quota")
		}
		return err
	}
	usage := tx.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", sent.MailboxID).Updates(map[string]any{
		"subject_bytes":    gorm.Expr("subject_bytes + ?", len(sent.Subject)),
		"body_bytes":       gorm.Expr("body_bytes + ?", len(sent.BodyText)+len(sent.BodyHTML)),
		"attachment_bytes": gorm.Expr("attachment_bytes + ?", attachmentBytes),
		"message_count":    gorm.Expr("message_count + 1"),
	})
	if usage.Error != nil {
		return usage.Error
	}
	if usage.RowsAffected != 1 {
		return errors.New("sender quota usage missing")
	}
	return nil
}

func (h *MessageHandler) saveSentParts(ctx context.Context, messageID uint, parts []pipeline.Attachment) error {
	for _, part := range parts {
		storage, ref, checksum, err := h.putIndependentPart(ctx, part)
		if err != nil {
			return err
		}
		row := models.Attachment{
			MessageID: messageID, Filename: part.Filename, ContentType: part.ContentType, SizeBytes: part.Size,
			StorageType: storage, StorageRef: ref, Checksum: checksum, Disposition: part.Disposition, ContentID: part.ContentID,
		}
		if err := h.db.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}
