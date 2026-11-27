package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type attemptPinKey struct{}

// SetMessageSources supplies the store for rows that refer to prepared messages.
func (w *Worker) SetMessageSources(sources *messagestore.Sources) {
	w.sources = sources
}

func queueHeaders(item models.OutboundQueue) string {
	if item.SourceRef != nil {
		return item.SourceHeaders
	}
	return item.RawMessage
}

func (w *Worker) claimSource(tx *gorm.DB, item *claimedItem) error {
	var source models.MessageSource
	ref := *item.SourceRef
	if err := tx.Select("size", "header_metadata").Where("ref = ?", ref).Take(&source).Error; err != nil {
		return err
	}
	item.sourceOctets = source.Size
	item.SourceHeaders = string(source.HeaderMetadata)
	if source.Size > w.maxSourceOctets {
		return nil
	}
	if w.sources == nil {
		return errors.New("queue: no message source store configured")
	}
	var err error
	item.pin, err = w.sources.PinAttempt(tx.Statement.Context, tx, ref)
	return err
}

func (w *Worker) releaseAttemptPin(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), heartbeatWriteTimeout)
	defer cancel()
	if err := messagestore.UnpinAttempt(ctx, w.db, id); err != nil {
		slog.ErrorContext(ctx, "queue: could not release message source attempt pin", "error", err)
	}
}

func (w *Worker) openSource(ctx context.Context, item models.OutboundQueue) (io.ReadCloser, int64, error) {
	if w.sources == nil {
		return nil, 0, errors.New("queue: no message source store configured")
	}
	reader, size, err := w.sources.Open(ctx, *item.SourceRef)
	if err != nil {
		return nil, 0, fmt.Errorf("open queued message source: %w", err)
	}
	if size > w.maxSourceOctets {
		_ = reader.Close()
		return nil, 0, errSourceTooLarge(size, w.maxSourceOctets)
	}
	return reader, size, nil
}

// writeAttemptOutcome keeps the queue row and its source ownership in the same
// transaction. A stale success can still deliver the row, but only the writer
// that clears the current reference may release it.
func (w *Worker) writeAttemptOutcome(ctx context.Context, item models.OutboundQueue, fenced bool, values map[string]interface{}) (int64, error) {
	target := func(db *gorm.DB) *gorm.DB {
		q := db.Model(&models.OutboundQueue{}).Where("id = ?", item.ID)
		if fenced {
			q = q.Where("status = ? AND attempts = ?", "delivering", item.Attempts)
		}
		return q
	}
	if item.SourceRef == nil || values["status"] == "deferred" {
		result := target(w.db).Updates(values)
		return result.RowsAffected, result.Error
	}
	var affected int64
	err := w.db.Transaction(func(tx *gorm.DB) error {
		var owner models.OutboundQueue
		result := target(tx).Select("source_ref").Clauses(clause.Locking{Strength: "UPDATE"}).Find(&owner)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		values["source_ref"] = nil
		result = target(tx).Updates(values)
		if result.Error != nil {
			return result.Error
		}
		affected = result.RowsAffected
		if affected == 1 && owner.SourceRef != nil {
			return messagestore.ReleaseReference(ctx, tx, *owner.SourceRef)
		}
		return nil
	})
	return affected, err
}
