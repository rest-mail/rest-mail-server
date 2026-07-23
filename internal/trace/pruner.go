package trace

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// Pruner enforces the per-message trace retention policy on message_traces. On a
// ticker it (1) deletes traces past their expires_at horizon (stamped by the
// recorder at CreatedAt + retention) and (2) enforces a hard row-count backstop
// (TRACE_MAX_ROWS): if the table exceeds the cap, the oldest rows beyond it are
// deleted.
//
// It NEVER touches pipeline_rollups. Aggregate history is long-lived and derived
// from the always-on counters, not from these sampled/pruned traces — so pruning
// a trace can never lose aggregate history. Rollups get their own (much longer /
// unbounded-by-default) retention; pruning them aggressively is deliberately not
// done here.
type Pruner struct {
	db       *gorm.DB
	interval time.Duration
	maxRows  int64 // hard backstop; 0 disables the row cap (only expiry applies)
	now      func() time.Time

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewPruner builds a trace pruner running every interval (production: hourly).
// maxRows is the row-count backstop (0 disables it).
func NewPruner(db *gorm.DB, interval time.Duration, maxRows int64) *Pruner {
	return &Pruner{
		db:       db,
		interval: interval,
		maxRows:  maxRows,
		now:      time.Now,
		stop:     make(chan struct{}),
	}
}

// Start launches the background prune loop.
func (p *Pruner) Start() {
	p.wg.Add(1)
	go p.loop()
	slog.Info("trace pruner started", "interval", p.interval, "max_rows", p.maxRows)
}

// Shutdown stops the prune loop.
func (p *Pruner) Shutdown() {
	close(p.stop)
	p.wg.Wait()
	slog.Info("trace pruner stopped")
}

func (p *Pruner) loop() {
	defer p.wg.Done()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.pruneOnce(p.now())
		}
	}
}

// pruneOnce deletes expired traces and enforces the row-count backstop. Errors
// are logged and swallowed — a failed prune must never crash the process; the
// next tick retries.
func (p *Pruner) pruneOnce(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := p.db.WithContext(ctx)

	// 1. Retention horizon: delete anything past its expires_at. Rows with a NULL
	// expires_at (e.g. pre-PR4 traces, or recorded with retention disabled) are
	// left to the row-count backstop.
	res := db.Where("expires_at IS NOT NULL AND expires_at < ?", now).Delete(&models.MessageTrace{})
	if res.Error != nil {
		slog.Warn("trace pruner: expiry delete failed", "error", res.Error)
	} else if res.RowsAffected > 0 {
		slog.Info("trace pruner: deleted expired traces", "count", res.RowsAffected)
	}

	// 2. Hard backstop: if the table still exceeds maxRows, delete the oldest rows
	// beyond the cap (oldest by created_at, then id for a stable tiebreak). This
	// guards against unbounded growth if sampling is disabled or volume spikes far
	// above the retention estimate.
	if p.maxRows <= 0 {
		return
	}
	var count int64
	if err := db.Model(&models.MessageTrace{}).Count(&count).Error; err != nil {
		slog.Warn("trace pruner: count failed", "error", err)
		return
	}
	if count <= p.maxRows {
		return
	}
	excess := count - p.maxRows
	oldest := db.Model(&models.MessageTrace{}).
		Order("created_at ASC, id ASC").
		Limit(int(excess)).
		Select("id")
	res = db.Where("id IN (?)", oldest).Delete(&models.MessageTrace{})
	if res.Error != nil {
		slog.Warn("trace pruner: backstop delete failed", "error", res.Error)
		return
	}
	slog.Info("trace pruner: backstop trimmed oldest traces",
		"deleted", res.RowsAffected, "max_rows", p.maxRows)
}
