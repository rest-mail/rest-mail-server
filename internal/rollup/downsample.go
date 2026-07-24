package rollup

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Multi-resolution downsampling keeps the self-contained analytics DB bounded
// over time. Fine rollups (pipeline_rollups, one row per ROLLUP_INTERVAL bucket)
// are kept at fine resolution only for a recent detailedRetention window; once a
// coarse period (default: a UTC day) has aged FULLY past that window, its fine
// buckets are SUMmed into a single coarse row (pipeline_rollups_coarse) and the
// superseded fine rows are deleted. That collapses ~288 five-minute rows per
// series per day into one daily row (≈288× reduction) with zero loss of the
// aggregate signal — the counts survive at coarser resolution instead of being
// retained (unbounded growth) or deleted (data loss).
//
// Correctness rests on three properties:
//   - Complete periods only: the cutoff is truncated to the coarse resolution, so
//     an eligible period always ENDS at or before now-detailedRetention. A period
//     that could still gain fine rows, or that is inside the hot window, is never
//     touched.
//   - Atomic per-period: each period's coarse-write and fine-delete commit in one
//     transaction. A crash mid-period rolls the whole period back (fine rows
//     intact, no partial coarse row), and re-running finds those fine rows and
//     redoes the period cleanly.
//   - Idempotent: once a period commits, its fine rows are gone, so a re-run never
//     re-aggregates them — no double-count. All state lives in the DB, so this
//     holds across worker restarts too.

// maxDownsamplePeriodsPerRun bounds how many coarse periods one pass condenses,
// so a large backlog (e.g. after a long outage) is worked off over several passes
// with bounded per-pass DB work rather than one unbounded transaction storm. At
// the hourly default cadence and 62 periods/pass, a two-month backlog clears in a
// single pass; anything larger catches up on subsequent ticks.
const maxDownsamplePeriodsPerRun = 62

// downsampleEnabled reports whether downsampling is configured. Both a positive
// detailed-retention window and a positive coarse resolution are required; either
// unset leaves the worker in fine-rollups-only mode (no behaviour change).
func (w *Worker) downsampleEnabled() bool {
	return w.detailedRetention > 0 && w.coarseResolution > 0
}

// downsampleLoop runs the downsampling pass on its own ticker. It runs one pass
// immediately on start to work off any backlog accumulated while the worker was
// down, then on each tick. It shares the worker's stop channel/WaitGroup with the
// snapshot loop.
func (w *Worker) downsampleLoop() {
	defer w.wg.Done()

	interval := w.downsampleInterval
	if interval <= 0 {
		interval = time.Hour // defensive: enabled but no cadence supplied
	}

	if err := w.downsampleOnce(w.now()); err != nil {
		slog.Warn("rollup worker: downsample pass failed", "error", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			if err := w.downsampleOnce(w.now()); err != nil {
				slog.Warn("rollup worker: downsample pass failed", "error", err)
			}
		}
	}
}

// downsampleOnce condenses every complete coarse period that has aged fully past
// the detailed-retention window (oldest first, up to maxDownsamplePeriodsPerRun),
// then prunes coarse rows past the optional coarse-retention cap. Safe to call
// repeatedly and concurrently with the snapshot loop (they touch disjoint rows:
// the snapshotter only writes the CURRENT bucket, which is far newer than any
// eligible period).
func (w *Worker) downsampleOnce(now time.Time) error {
	if !w.downsampleEnabled() {
		return nil
	}
	coarse := w.coarseResolution

	// Fine buckets that START before this cutoff are eligible. Truncating the
	// cutoff to the coarse resolution guarantees any period we select is COMPLETE:
	// its end is <= cutoff <= now-detailedRetention, so it can gain no further fine
	// rows and lies wholly outside the hot window.
	cutoff := now.UTC().Add(-w.detailedRetention).Truncate(coarse)

	for i := 0; i < maxDownsamplePeriodsPerRun; i++ {
		done, err := w.downsampleNextPeriod(cutoff, coarse)
		if err != nil {
			return err
		}
		if done {
			break
		}
	}

	w.pruneCoarse(now)
	return nil
}

// downsampleNextPeriod condenses the single OLDEST eligible coarse period in one
// transaction and returns done=true when no eligible fine rows remain. Each call
// strictly advances (the period it processes has its fine rows deleted on commit,
// so the next call's MIN(bucket_start) moves forward), so the bounded loop in
// downsampleOnce always terminates.
func (w *Worker) downsampleNextPeriod(cutoff time.Time, coarse time.Duration) (done bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Oldest eligible fine bucket. NULL (no eligible rows) → nothing left to do.
	var oldest sql.NullTime
	if err := w.db.WithContext(ctx).Model(&models.PipelineRollup{}).
		Where("bucket_start < ?", cutoff).
		Select("MIN(bucket_start)").
		Row().Scan(&oldest); err != nil {
		return false, err
	}
	if !oldest.Valid {
		return true, nil
	}

	periodStart := oldest.Time.UTC().Truncate(coarse)
	periodEnd := periodStart.Add(coarse)
	coarseSeconds := int(coarse / time.Second)

	txErr := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Aggregate this period's fine rows by series (metric_name + canonical
		// jsonb labels). Postgres normalises jsonb, so equal label sets group
		// together regardless of the fine rows' exact byte form.
		type agg struct {
			MetricName string          `gorm:"column:metric_name"`
			Labels     json.RawMessage `gorm:"column:labels"`
			Sum        float64         `gorm:"column:sum"`
		}
		var aggs []agg
		if err := tx.Model(&models.PipelineRollup{}).
			Select("metric_name, labels, SUM(value) AS sum").
			Where("bucket_start >= ? AND bucket_start < ?", periodStart, periodEnd).
			Group("metric_name, labels").
			Scan(&aggs).Error; err != nil {
			return err
		}

		for _, a := range aggs {
			row := models.PipelineRollupCoarse{
				MetricName:    a.MetricName,
				Labels:        a.Labels,
				BucketStart:   periodStart,
				BucketSeconds: coarseSeconds,
				Value:         a.Sum,
			}
			// Accumulate on conflict: harmless for the normal path (the fine rows
			// are deleted in this same transaction, so a committed period is never
			// re-aggregated) and correct if the same coarse period is ever built
			// across separate transactions.
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "metric_name"}, {Name: "labels"}, {Name: "bucket_start"}},
				DoUpdates: clause.Assignments(map[string]interface{}{
					"value": gorm.Expr("pipeline_rollups_coarse.value + ?", a.Sum),
				}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}

		// Test-only crash injection between the coarse write and the fine delete.
		if w.onBeforeFineDelete != nil {
			if err := w.onBeforeFineDelete(); err != nil {
				return err
			}
		}

		// Delete the now-superseded fine rows, atomically with the coarse write:
		// either the period is fully condensed or nothing changed.
		if err := tx.Where("bucket_start >= ? AND bucket_start < ?", periodStart, periodEnd).
			Delete(&models.PipelineRollup{}).Error; err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		return false, txErr
	}
	slog.Debug("rollup worker: condensed coarse period",
		"period_start", periodStart, "coarse_seconds", coarseSeconds)
	return false, nil
}

// pruneCoarse enforces the optional coarse-retention cap: coarse rows whose bucket
// starts before now-coarseRetention are deleted. A non-positive coarseRetention
// disables the cap (aggregate history kept indefinitely — the design's
// long-retention posture). Errors are logged and swallowed; the next pass retries.
func (w *Worker) pruneCoarse(now time.Time) {
	if w.coarseRetention <= 0 {
		return
	}
	horizon := now.UTC().Add(-w.coarseRetention)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res := w.db.WithContext(ctx).
		Where("bucket_start < ?", horizon).
		Delete(&models.PipelineRollupCoarse{})
	if res.Error != nil {
		slog.Warn("rollup worker: coarse prune failed", "error", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		slog.Info("rollup worker: pruned coarse rollups past retention",
			"deleted", res.RowsAffected, "retention", w.coarseRetention)
	}
}
