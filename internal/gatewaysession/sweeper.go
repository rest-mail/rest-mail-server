// Package gatewaysession runs the background upkeep of the protocol gateways'
// connection sessions (#1586).
package gatewaysession

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/tracing"
	"gorm.io/gorm"
)

// SweepInterval is how often the API closes idle gateway sessions.
const SweepInterval = time.Hour

// Sweeper closes, on a ticker, every gateway session left unused for longer
// than models.GatewaySessionIdle. A session presented after that is already
// refused by the per-request check; the sweep closes the ones never presented
// again (a gateway that died without closing them), so open sessions in the
// table are the ones that can still be used.
type Sweeper struct {
	db       *gorm.DB
	interval time.Duration
	now      func() time.Time

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewSweeper builds a sweeper running every interval (production: SweepInterval).
func NewSweeper(db *gorm.DB, interval time.Duration) *Sweeper {
	return &Sweeper{db: db, interval: interval, now: time.Now, stop: make(chan struct{})}
}

// Start launches the background sweep loop.
func (s *Sweeper) Start() {
	s.wg.Add(1)
	go s.loop()
	slog.Info("gateway session sweeper started", "interval", s.interval)
}

// Shutdown stops the sweep loop.
func (s *Sweeper) Shutdown() {
	close(s.stop)
	s.wg.Wait()
	slog.Info("gateway session sweeper stopped")
}

func (s *Sweeper) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			_ = tracing.Job(context.Background(), "job.gateway_session_sweep", func(ctx context.Context) error {
				s.SweepOnce(ctx, s.now())
				return nil
			})
		}
	}
}

// SweepOnce closes every idle gateway session and returns how many it closed.
// Errors are logged, never fatal: the next tick retries.
func (s *Sweeper) SweepOnce(ctx context.Context, now time.Time) int64 {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	closed, err := repositories.NewGatewaySessionRepository(s.db.WithContext(ctx)).CloseIdle(now)
	if err != nil {
		slog.WarnContext(ctx, "gateway session sweeper: closing idle sessions failed", "error", err)
		return 0
	}
	if closed > 0 {
		slog.InfoContext(ctx, "gateway session sweeper: closed idle sessions", "count", closed)
	}
	return closed
}
