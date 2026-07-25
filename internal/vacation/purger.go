// Package vacation provides background maintenance for the vacation responder's
// storage. The vacation filter (internal/pipeline/filters/vacation.go) inserts a
// vacation_responses row for every (mailbox, sender) it auto-replies to, keyed by
// responded_at, and uses it only to deduplicate replies within a short window
// (RFC 5230, default 7 days). Nothing ever removed those rows, so the table grew
// without bound; the Purger here deletes rows older than the retention horizon on
// a ticker so the table stays bounded (issue #201).
package vacation

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// DefaultRetention is how long a vacation_responses row is kept. It comfortably
// exceeds the RFC 5230 default dedup window (7 days) and typical out-of-office
// periods, so a row still useful for dedup is never purged early, while stale
// rows are reclaimed.
const DefaultRetention = 30 * 24 * time.Hour

// Purger deletes vacation_responses rows past the retention horizon on a ticker.
// A row is expired when its responded_at is older than `retention` before now.
type Purger struct {
	db        *gorm.DB
	interval  time.Duration
	retention time.Duration
	now       func() time.Time

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewPurger builds a vacation-responses purger running every interval
// (production: hourly), deleting rows whose responded_at is older than retention.
// A non-positive retention falls back to DefaultRetention.
func NewPurger(db *gorm.DB, interval, retention time.Duration) *Purger {
	if retention <= 0 {
		retention = DefaultRetention
	}
	return &Purger{
		db:        db,
		interval:  interval,
		retention: retention,
		now:       time.Now,
		stop:      make(chan struct{}),
	}
}

// Start launches the background purge loop.
func (p *Purger) Start() {
	p.wg.Add(1)
	go p.loop()
	slog.Info("vacation purger started", "interval", p.interval, "retention", p.retention)
}

// Shutdown stops the purge loop.
func (p *Purger) Shutdown() {
	close(p.stop)
	p.wg.Wait()
	slog.Info("vacation purger stopped")
}

func (p *Purger) loop() {
	defer p.wg.Done()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.PurgeOnce(p.now())
		}
	}
}

// PurgeOnce deletes every vacation_responses row older than the retention horizon
// and returns the number of rows removed. Errors are logged and swallowed — a
// failed purge must never crash the process; the next tick retries.
func (p *Purger) PurgeOnce(now time.Time) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cutoff := now.Add(-p.retention)
	res := p.db.WithContext(ctx).
		Where("responded_at < ?", cutoff).
		Delete(&models.VacationResponse{})
	if res.Error != nil {
		slog.Warn("vacation purger: delete failed", "error", res.Error)
		return 0
	}
	if res.RowsAffected > 0 {
		slog.Info("vacation purger: deleted expired responses", "count", res.RowsAffected)
	}
	return res.RowsAffected
}
