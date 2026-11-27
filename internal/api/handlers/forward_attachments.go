package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
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
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
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
	if h.forwardStore != nil {
		source := &reopeningReader{size: part.Size, open: func() (io.ReadCloser, error) {
			reader, err := h.openCheckedPart(ctx, part)
			if err != nil {
				return nil, err
			}
			return reader, nil
		}}
		defer source.Close()
		ref, checksum, err = h.forwardStore.PutIndependentReader(ctx, source, part.Size)
		storage = "s3"
	} else {
		// The checked part fails the copy unless it is exactly the part's octets.
		var source *checkedPart
		if source, err = h.openCheckedPart(ctx, part); err != nil {
			return "", "", "", err
		}
		defer source.Close()
		ref, checksum, _, err = attachmentstore.PutIndependentFilesystemReader(ctx, h.forwardRoot, source)
		storage = "filesystem"
	}
	// The upload reads the part to its size and no further, so only its checksum can
	// show a part that is longer.
	if err == nil && part.Checksum != "" && checksum != part.Checksum {
		err = errForwardAttachment
	}
	return storage, ref, checksum, err
}

// reopeningReader is a part's bytes as an io.ReadSeeker, for an upload that reads them for
// their checksum and then again to send them: seeking opens the part again, so it is never
// held whole.
type reopeningReader struct {
	open      func() (io.ReadCloser, error)
	size, pos int64
	r         io.ReadCloser
	rpos      int64
}

func (s *reopeningReader) Read(p []byte) (int, error) {
	if s.pos >= s.size {
		return 0, io.EOF
	}
	if s.r == nil || s.rpos != s.pos {
		if err := s.Close(); err != nil {
			return 0, err
		}
		r, err := s.open()
		if err != nil {
			return 0, err
		}
		s.r, s.rpos = r, 0
		if s.pos > 0 {
			skipped, err := io.CopyN(io.Discard, r, s.pos)
			s.rpos = skipped
			if err != nil {
				return 0, err
			}
		}
	}
	if left := s.size - s.pos; int64(len(p)) > left {
		p = p[:left]
	}
	n, err := s.r.Read(p)
	s.pos += int64(n)
	s.rpos += int64(n)
	if err == io.EOF && s.pos < s.size {
		err = io.ErrUnexpectedEOF
	} else if err == io.EOF {
		err = nil
	}
	return n, err
}

func (s *reopeningReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += s.pos
	case io.SeekEnd:
		offset += s.size
	default:
		return 0, errors.New("invalid seek mode")
	}
	if offset < 0 {
		return 0, errors.New("negative offset")
	}
	s.pos = offset
	return offset, nil
}

func (s *reopeningReader) Close() error {
	if s.r == nil {
		return nil
	}
	err := s.r.Close()
	s.r = nil
	return err
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
		// The checked bytes are the part from here on, whatever it referred to.
		parts[i].Storage, parts[i].Ref = "", ""
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
			// The Sent copy's own object, decoded from the checked content as it is
			// stored: the part is not held a second time.
			storage, ref, checksum, err := h.putIndependentPart(r.Context(), part)
			if err != nil {
				return err
			}
			row := models.Attachment{MessageID: sent.ID, Filename: part.Filename, ContentType: part.ContentType, SizeBytes: part.Size, StorageType: storage, StorageRef: ref, Checksum: checksum, Disposition: part.Disposition, ContentID: part.ContentID}
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
		reader, err := h.openForwardPart(ctx, part)
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
