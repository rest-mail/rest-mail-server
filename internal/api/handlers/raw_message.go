package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
	"gorm.io/gorm"
)

// rawSourceChunk is the most of a stored source the raw route reads from the database at
// once. The route holds one chunk at a time while it writes, so what it keeps in memory does
// not grow with the message (#2107).
const rawSourceChunk = 1 << 20

// rawSourceSQL is a message's stored source as octets: raw_octets when the row keeps the
// source there, otherwise raw_message in UTF-8, the same octets models.Message.Source gives.
const rawSourceSQL = `coalesce(nullif(raw_octets, ''::bytea), convert_to(coalesce(raw_message, ''), 'UTF8'))`

// rawSourceLength is the octet count of message id's stored source, 0 when it keeps none.
func rawSourceLength(db *gorm.DB, id uint) (int64, error) {
	var n int64
	err := db.Raw(`SELECT octet_length(`+rawSourceSQL+`) FROM messages WHERE id = ?`, id).Row().Scan(&n)
	return n, err
}

// rawSourceRange reads up to n octets of message id's stored source, starting at offset (0
// based).
func rawSourceRange(db *gorm.DB, id uint, offset, n int64) ([]byte, error) {
	var chunk []byte
	err := db.Raw(`SELECT substring(`+rawSourceSQL+` FROM ? FOR ?) FROM messages WHERE id = ?`, offset+1, n, id).Row().Scan(&chunk)
	return chunk, err
}

// streamRawSource writes size octets of message id's stored source to w, one chunk at a
// time. A chunk that cannot be read, or comes back short (the message went away while it was
// being sent), ends the answer early: the declared Content-Length is then not met, so the
// client sees a truncated response, never a different message.
func streamRawSource(ctx context.Context, db *gorm.DB, id uint, size int64, w io.Writer) error {
	for offset := int64(0); offset < size; {
		want := min(size-offset, rawSourceChunk)
		chunk, err := rawSourceRange(db.WithContext(ctx), id, offset, want)
		if err != nil {
			return fmt.Errorf("read source of message %d at %d: %w", id, offset, err)
		}
		if int64(len(chunk)) != want {
			return fmt.Errorf("source of message %d changed while sent: %d octets at %d, want %d", id, len(chunk), offset, want)
		}
		if _, err := w.Write(chunk); err != nil {
			return err
		}
		offset += want
	}
	return nil
}

// rawMessageStream answers the raw route with message id's stored source of size octets,
// read in chunks as it is written.
func rawMessageStream(ctx context.Context, db *gorm.DB, id uint, size int64) func(io.Writer, func()) {
	return sendRawMessage(ctx, id, func(w io.Writer) error {
		return streamRawSource(ctx, db, id, size, w)
	})
}

func sendRawMessage(ctx context.Context, id uint, send func(io.Writer) error) func(io.Writer, func()) {
	return func(w io.Writer, _ func()) {
		if err := send(w); err != nil {
			if ctx.Err() == nil {
				slog.ErrorContext(ctx, "raw message: source not sent", "message_id", id, "error", err)
			}
			// The status and Content-Length are already on the wire: abort the
			// connection so the client cannot take a short body for the message.
			panic(http.ErrAbortHandler)
		}
	}
}

func (h *MessageHandler) openMessageSource(ctx context.Context, id uint, ref string) (io.ReadCloser, int64, error) {
	if h.messageSources == nil {
		return nil, 0, messagestore.ErrSourceUnavailable
	}
	return h.messageSources.OpenMessage(ctx, id, ref)
}

func (h *MessageHandler) referencedRawMessage(ctx context.Context, id uint, ref string) (endpoint.Stream, error) {
	body, size, err := h.openMessageSource(ctx, id, ref)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return endpoint.Stream{}, errMessageNotFound
	}
	if err != nil {
		return endpoint.Stream{}, fmt.Errorf("open message source: %w", err)
	}
	return endpoint.Stream{
		ContentType: "message/rfc822",
		Header:      rawMessageHeader(size),
		Done:        func() { _ = body.Close() },
		Send: sendRawMessage(ctx, id, func(w io.Writer) error {
			_, err := io.Copy(w, body)
			return err
		}),
	}, nil
}

// rawMessageHeader is the Content-Length the raw route declares for a source of size octets.
func rawMessageHeader(size int64) http.Header {
	return http.Header{"Content-Length": []string{strconv.FormatInt(size, 10)}}
}
