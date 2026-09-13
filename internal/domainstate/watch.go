// Package domainstate keeps a domain's live/not-live state honest about whether
// it can actually be served.
//
// Mail for a domain is only ever carried over TLS, so a live domain whose
// certificate has stopped being valid cannot be served correctly: its handshakes
// are refused rather than answered with another name's certificate (issue #290).
// Leaving it marked live hides that. The watcher takes such a domain out of
// service and says so loudly, so the state in the database matches what the
// gateways can do (issue #289).
package domainstate

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/metrics"
	"gorm.io/gorm"
)

// DefaultInterval is how often the watcher looks, when none is given.
const DefaultInterval = time.Hour

// Watcher takes live domains out of service when their certificate stops being
// valid.
type Watcher struct {
	db       *gorm.DB
	interval time.Duration
	stop     chan struct{}
}

// NewWatcher creates a watcher over db. An interval of zero means DefaultInterval.
func NewWatcher(db *gorm.DB, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Watcher{db: db, interval: interval, stop: make(chan struct{})}
}

// Start runs the watcher until Shutdown is called.
func (w *Watcher) Start() {
	go w.run()
}

// Shutdown stops the watcher.
func (w *Watcher) Shutdown() {
	close(w.stop)
}

func (w *Watcher) run() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := w.Check(); err != nil {
				slog.Error("domainstate: checking which live domains can still be served failed", "error", err)
			}
		case <-w.stop:
			return
		}
	}
}

// Check takes every live domain without a currently valid certificate out of
// service, and reports how many it took out.
//
// "Currently valid" is the same rule the gateways serve by: a stored certificate
// for that domain whose validity window covers now. A domain that is already out
// of service is left alone, so a domain is counted once, when it leaves service,
// and not again on every pass.
func (w *Watcher) Check() (int, error) {
	var unservable []models.Domain
	err := w.db.Model(&models.Domain{}).
		Where("active = ?", true).
		Where(`NOT EXISTS (
			SELECT 1 FROM certificates c
			WHERE c.domain_id = domains.id
			  AND c.not_before <= NOW()
			  AND c.not_after > NOW()
		)`).
		Find(&unservable).Error
	if err != nil {
		return 0, fmt.Errorf("finding live domains without a valid certificate: %w", err)
	}

	taken := 0
	for _, d := range unservable {
		if err := w.db.Model(&models.Domain{}).Where("id = ?", d.ID).Update("active", false).Error; err != nil {
			slog.Error("domainstate: could not take a domain out of service",
				"domain", d.Name, "error", err)
			continue
		}
		metrics.DomainsOutOfService.Inc()
		slog.Error("domainstate: domain taken out of service, it has no currently valid certificate and cannot be served",
			"domain", d.Name)
		taken++
	}
	return taken, nil
}
