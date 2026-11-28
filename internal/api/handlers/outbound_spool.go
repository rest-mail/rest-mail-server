package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/outbound"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

var errComposedTooLarge = endpoint.Refuse(http.StatusRequestEntityTooLarge, "message_too_large", "The signed message exceeds the message size limit")
var errSendDeferred = endpoint.Refuse(http.StatusServiceUnavailable, "deferred", "Try again later")

// sendTransaction reserves an upload key before taking any owner locks. A
// draft, held send or receipt can hold its lock through rendering and upload
// without acquiring a second database connection. An upload whose owner
// rolls back is still tracked by the reservation.
func (h *MessageHandler) sendTransaction(ctx context.Context, send func(*MessageHandler) error) error {
	sender := *h
	if h.messageSources != nil {
		ref, err := h.messageSources.ReserveStage(ctx)
		if err != nil {
			return err
		}
		sender.sourceStage = ref
		defer func() {
			if !sender.sourceStageUsed {
				_ = h.messageSources.DiscardUnusedStage(ctx, ref)
			}
		}()
	}
	return h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sender.db = tx
		return send(&sender)
	})
}

type outboundAttachmentSource func() (io.ReadCloser, error)

func (open outboundAttachmentSource) Open() (io.ReadCloser, error) { return open() }

var composedLineEnds = strings.NewReplacer("\r\r\n", "\r\n", "\r\n", "\r\n", "\r", "\r\n", "\n", "\r\n")

func smtpText(text string) string {
	if rmail.HasBareLineEnds(text) {
		return composedLineEnds.Replace(text)
	}
	return text
}

func (h *MessageHandler) renderComposed(ctx context.Context, dst io.Writer, m outboundMessage) error {
	// A generated multipart has text and base64 parts. Normalize its text
	// before signing so the SMTP DATA path can stream the same MIME bytes.
	m.BodyText, m.BodyHTML = smtpText(m.BodyText), smtpText(m.BodyHTML)
	email := outboundEmail(m)
	opts := rmime.SerializeOptions{
		AttachmentSources: make([]rmime.AttachmentSource, 0, len(email.Attachments)),
		InlineSources:     make([]rmime.AttachmentSource, 0, len(email.Inline)),
	}
	for _, part := range m.Attachments {
		open := outboundAttachmentSource(func() (io.ReadCloser, error) {
			return h.openCheckedPart(ctx, part)
		})
		if part.Disposition == "inline" {
			opts.InlineSources = append(opts.InlineSources, open)
		} else {
			opts.AttachmentSources = append(opts.AttachmentSources, open)
		}
	}
	if m.ICS != nil {
		opts.AttachmentSources = append(opts.AttachmentSources, outboundAttachmentSource(func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(m.ICS)), nil
		}))
	}
	return rmime.SerializeTo(dst, email, opts)
}

// composedSpoolWriter stops rendering at the unsigned ceiling. The signature
// is charged separately once its length is known.
type composedSpoolWriter struct {
	ctx  context.Context
	file *os.File
	left int64
}

func (w *composedSpoolWriter) Write(p []byte) (int, error) {
	if err := w.allow(len(p)); err != nil {
		return 0, err
	}
	n, err := w.file.Write(p)
	w.left -= int64(n)
	return n, err
}

func (w *composedSpoolWriter) WriteString(s string) (int, error) {
	if err := w.allow(len(s)); err != nil {
		return 0, err
	}
	n, err := w.file.WriteString(s)
	w.left -= int64(n)
	return n, err
}

func (w *composedSpoolWriter) allow(n int) error {
	if int64(n) > w.left {
		return errComposedTooLarge
	}
	return w.ctx.Err()
}

type composedSource struct {
	io.Reader
	size    int64
	headers []byte
}

type composedSpoolReader struct {
	ctx context.Context
	io.Reader
}

func (r composedSpoolReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (h *MessageHandler) spoolComposed(ctx context.Context, m outboundMessage, sent *models.Message, recipients [][]string, requireTLS, requireSignature bool) error {
	partBytes, err := h.prepareComposedParts(ctx, m.Attachments)
	if err != nil {
		return err
	}
	return h.withComposedSource(ctx, m, requireSignature, func(source composedSource) error {
		if m.CalendarReply != "" {
			sent.SizeBytes = int(source.size)
		}
		return h.persistComposed(ctx, source, sent, m.Attachments, partBytes, recipients, requireTLS)
	})
}

func (h *MessageHandler) withComposedSource(ctx context.Context, m outboundMessage, requireSignature bool, use func(composedSource) error) error {
	file, closeSpool, err := h.openOutboundSpool("restmail-compose-*")
	if err != nil {
		return err
	}
	defer closeSpool()
	w := composedSpoolWriter{ctx: ctx, file: file, left: h.maxForwardBytes}
	if err := h.renderComposed(ctx, &w, m); err != nil {
		return err
	}
	size := h.maxForwardBytes - w.left
	return h.signComposedSpool(ctx, file, size, rmail.DomainFromAddress(m.From), requireSignature, use)
}

func (h *MessageHandler) openOutboundSpool(pattern string) (*os.File, func(), error) {
	if h.messageSpoolDir != "" {
		if err := os.MkdirAll(h.messageSpoolDir, 0o750); err != nil {
			return nil, nil, err
		}
	}
	file, err := os.CreateTemp(h.messageSpoolDir, pattern)
	if err != nil {
		return nil, nil, err
	}
	removeErr := os.Remove(file.Name())
	closeSpool := func() {
		_ = file.Close()
		if removeErr != nil {
			_ = os.Remove(file.Name())
		}
	}
	return file, closeSpool, nil
}

func (h *MessageHandler) signComposedSpool(ctx context.Context, file *os.File, size int64, domain string, requireSignature bool, use func(composedSource) error) error {
	// Read only the header section from the start of the generated CRLF source.
	// The cap includes the separator and the signature added below.
	headers := make([]byte, min(size, messagestore.MaxHeaderMetadataBytes))
	if _, err := file.ReadAt(headers, 0); err != nil {
		return err
	}
	end := bytes.Index(headers, []byte("\r\n\r\n"))
	if end < 0 {
		return errComposedTooLarge
	}
	headers = headers[:end+4]
	reader := composedSpoolReader{ctx: ctx, Reader: io.NewSectionReader(file, 0, size)}
	signature, err := outbound.DKIMSignature(ctx, h.db, h.masterKey, domain, reader)
	if err != nil || (requireSignature && signature == "") {
		return errSendDeferred
	}
	if int64(len(signature)) > h.maxForwardBytes-size || len(signature)+len(headers) > messagestore.MaxHeaderMetadataBytes {
		return errComposedTooLarge
	}
	if signature != "" {
		headers = append([]byte(signature), headers...)
	}
	return use(composedSource{
		Reader:  io.MultiReader(strings.NewReader(signature), io.NewSectionReader(file, 0, size)),
		size:    size + int64(len(signature)),
		headers: headers,
	})
}

// persistComposed gives the Sent copy and each pending recipient one reference
// to the same signed source. The attachment rows and their quota share that
// transaction.
func (h *MessageHandler) persistComposed(ctx context.Context, source composedSource, sent *models.Message, parts []pipeline.Attachment, partBytes int64, recipients [][]string, requireTLS bool) error {
	if h.messageSources == nil {
		return errors.New("composed send has no message source store")
	}
	publish := func(sender *MessageHandler) error {
		tx, ref := sender.db, sender.sourceStage
		sender.sourceStageUsed = true
		if _, err := sender.messageSources.WriteStage(ctx, tx, ref, source, source.size, source.headers); err != nil {
			return err
		}
		var owners int64 = 1 // Sent
		for _, group := range recipients {
			owners += int64(len(group))
		}
		if err := sender.messageSources.Publish(ctx, tx, ref, owners); err != nil {
			return err
		}
		sent.SourceRef = &ref
		sent.HasAttachments = len(parts) > 0
		if parts != nil {
			if err := reserveForwardStorage(tx, sent, partBytes); err != nil {
				return err
			}
		}
		if err := sender.messageSources.CreateMessage(ctx, tx, sent); err != nil {
			return err
		}
		if err := sender.saveSentParts(ctx, sent.ID, parts); err != nil {
			return err
		}
		return queueComposed(sent, recipients, requireTLS, func(rows []models.OutboundQueue) error {
			return tx.Create(&rows).Error
		})
	}
	if h.sourceStage != "" {
		return publish(h)
	}
	return h.sendTransaction(ctx, publish)
}

// Use one bounded SQL batch at a time. The caller already holds the recipient
// list, and each row carries only its envelope and a reference.
func queueComposed(sent *models.Message, recipients [][]string, requireTLS bool, insert func([]models.OutboundQueue) error) error {
	const batchSize = 64
	rows := make([]models.OutboundQueue, 0, batchSize)
	for _, group := range recipients {
		for _, recipient := range group {
			rows = append(rows, models.OutboundQueue{
				MessageID: &sent.ID, Sender: sent.Sender, Recipient: recipient,
				Domain: rmail.DomainFromAddress(recipient), SourceRef: sent.SourceRef,
				Status: "pending", RequireTLS: requireTLS,
			})
			if len(rows) == batchSize {
				if err := insert(rows); err != nil {
					return err
				}
				rows = rows[:0]
			}
		}
	}
	if len(rows) != 0 {
		return insert(rows)
	}
	return nil
}
