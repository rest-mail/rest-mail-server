package sendidempotency

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

const Retention = 24 * time.Hour

type Purger struct {
	db       *gorm.DB
	interval time.Duration
	stop     chan struct{}
	wg       sync.WaitGroup
}

func NewPurger(db *gorm.DB, interval time.Duration) *Purger {
	return &Purger{db: db, interval: interval, stop: make(chan struct{})}
}

func (p *Purger) Start() {
	p.wg.Go(func() {
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case now := <-ticker.C:
				p.PurgeOnce(now)
			}
		}
	})
}

func (p *Purger) Shutdown() {
	close(p.stop)
	p.wg.Wait()
}

func (p *Purger) PurgeOnce(now time.Time) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result := p.db.WithContext(ctx).Where("expires_at <= ?", now).Delete(&models.SendIdempotency{})
	if result.Error != nil {
		slog.Warn("send idempotency purger: delete failed", "error", result.Error)
		return 0
	}
	return result.RowsAffected
}
