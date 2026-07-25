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
// the past. Rows written before the ExpiresAt column existed carry a NULL
// horizon; those are collected by a created_at fallback (CreatedAt older than the
// default TTL) so no row can outlive the retention policy regardless of when it
// was written.
type Purger struct {
	db       *gorm.DB
	interval time.Duration
	// fallbackTTL bounds NULL-horizon (legacy) rows by their created_at.
	fallbackTTL time.Duration
	now         func() time.Time

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewPurger builds a greylist purger running every interval (production:
// hourly). Legacy rows with a NULL expires_at are collected once their created_at
// is older than models.DefaultGreylistTTLDays.
func NewPurger(db *gorm.DB, interval time.Duration) *Purger {
	return &Purger{
		db:          db,
		interval:    interval,
		fallbackTTL: time.Duration(models.DefaultGreylistTTLDays) * 24 * time.Hour,
		now:         time.Now,
		stop:        make(chan struct{}),
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

	var deleted int64

	// Entries with an explicit horizon in the past.
	res := db.Where("expires_at IS NOT NULL AND expires_at < ?", now).Delete(&models.GreylistEntry{})
	if res.Error != nil {
		slog.Warn("greylist purger: expiry delete failed", "error", res.Error)
	} else {
		deleted += res.RowsAffected
	}

	// Legacy rows with no horizon: bound them by created_at + default TTL so a
	// pre-migration table can never grow without bound either.
	fallbackCutoff := now.Add(-p.fallbackTTL)
	res = db.Where("expires_at IS NULL AND created_at < ?", fallbackCutoff).Delete(&models.GreylistEntry{})
	if res.Error != nil {
		slog.Warn("greylist purger: fallback delete failed", "error", res.Error)
	} else {
		deleted += res.RowsAffected
	}

	if deleted > 0 {
		slog.Info("greylist purger: deleted expired entries", "count", deleted)
	}
	return deleted
}
