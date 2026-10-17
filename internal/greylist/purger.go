// Package greylist provides the background maintenance for the greylist filter's
// storage table. The filter (internal/pipeline/filters/greylist.go) stamps every
// greylist_entries row with an ExpiresAt horizon derived from its ttl_days
// config; the Purger here deletes rows past that horizon on a ticker so the table
// stays bounded.
package greylist

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// Purger deletes expired greylist_entries rows on a ticker. Without it every new
// sender/recipient/IP triple inserts a row that is never removed, so the table
// grows without bound.
//
// A row is expired when its ExpiresAt (stamped by the filter from ttl_days) is in
// the past.
type Purger struct {
	db       *gorm.DB
	interval time.Duration
	now      func() time.Time

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewPurger builds a greylist purger running every interval (production:
// hourly).
func NewPurger(db *gorm.DB, interval time.Duration) *Purger {
	return &Purger{
		db:       db,
		interval: interval,
		now:      time.Now,
		stop:     make(chan struct{}),
	}
}

// Start launches the background purge loop.
func (p *Purger) Start() {
	p.wg.Add(1)
	go p.loop()
	slog.Info("greylist purger started", "interval", p.interval)
}

// Shutdown stops the purge loop.
func (p *Purger) Shutdown() {
	close(p.stop)
	p.wg.Wait()
	slog.Info("greylist purger stopped")
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

// PurgeOnce deletes every greylist entry past its TTL horizon and returns the
// number of rows removed. Errors are logged and swallowed — a failed purge must
// never crash the process; the next tick retries.
func (p *Purger) PurgeOnce(now time.Time) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := p.db.WithContext(ctx)

	res := db.Where("expires_at < ?", now).Delete(&models.GreylistEntry{})
	if res.Error != nil {
		slog.Warn("greylist purger: expiry delete failed", "error", res.Error)
		return 0
	}
	deleted := res.RowsAffected
	if deleted > 0 {
		slog.Info("greylist purger: deleted expired entries", "count", deleted)
	}
	return deleted
}
