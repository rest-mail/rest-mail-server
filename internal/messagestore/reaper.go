package messagestore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	reapInterval = time.Minute
	reapBatch    = 100
)

// RunReaper sweeps until ctx is cancelled. Multiple processes can run it.
func (s *Sources) RunReaper(ctx context.Context) {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep, cancel := context.WithTimeout(ctx, reapInterval)
			_, err := s.Reap(sweep)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.ErrorContext(ctx, "message source cleanup failed", "error", err)
			}
		}
	}
}

// Reap removes up to a batch of expired stages and released objects. A failed
// object deletion delays its retry, so it cannot hold up later candidates.
func (s *Sources) Reap(ctx context.Context) (int, error) {
	var removed int
	var failures error
	db := s.db.WithContext(ctx)
	if err := reapAttempts(ctx, db); err != nil {
		return 0, err
	}
	for range reapBatch {
		row, err := claimSourceDeletion(db)
		if err != nil || row.Ref == "" {
			return removed, errors.Join(failures, err)
		}
		if err := s.deleteSourceObject(ctx, row.Ref); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		result := removeDeletedSource(db, row.Ref)
		if result.Error != nil {
			return removed, errors.Join(failures, result.Error)
		}
		removed += int(result.RowsAffected)
	}
	return removed, failures
}

// DeleteResetSources removes objects whose ledger rows the test reset has
// already committed as deleting. Missing objects are safe to delete again.
func (s *Sources) DeleteResetSources(ctx context.Context, db *gorm.DB, refs []string) error {
	for _, ref := range refs {
		if err := s.deleteSourceObject(ctx, ref); err != nil {
			return err
		}
		if err := removeDeletedSource(db.WithContext(ctx), ref).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *Sources) deleteSourceObject(ctx context.Context, ref string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.store.Delete(ctx, ref); err != nil {
		return fmt.Errorf("delete message source %s: %w", ref, err)
	}
	return nil
}

func removeDeletedSource(db *gorm.DB, ref string) *gorm.DB {
	return db.Where("state = ? AND reference_count = 0", models.MessageSourceDeleting).Delete(&models.MessageSource{Ref: ref})
}

func claimSourceDeletion(db *gorm.DB) (models.MessageSource, error) {
	var row models.MessageSource
	err := db.Transaction(func(tx *gorm.DB) error {
		err := tx.Select("ref").Where("reference_count = 0 AND reap_at <= statement_timestamp()").
			Order("reap_at").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		// Commit an irreversible state before touching storage. An ambiguous
		// commit, failed deletion or crash just leaves the next sweep to retry.
		return sourceUpdate(tx.Model(&row).Updates(map[string]any{
			"state":   models.MessageSourceDeleting,
			"reap_at": gorm.Expr("clock_timestamp() + interval '1 minute'"),
		}))
	})
	return row, err
}
