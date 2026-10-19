package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/restmail/restmail/internal/dnsbl"
	"gorm.io/gorm"
)

// dnsblStopGrace is how long stop waits past the check timeout for the
// monitor to return. Cancelling abandons the check in flight, so Run returns
// almost at once; this only bounds a resolver that ignores its context.
const dnsblStopGrace = 5 * time.Second

// dnsblRuntime is the DNSBL monitor as the API runs it (issue #148). It is off
// unless the operator's config file enables it. After database migration,
// prepare restores its state and installs the durable observer before start.
type dnsblRuntime struct {
	cfg      dnsbl.MonitorConfig
	checker  dnsbl.IPChecker
	monitor  *dnsbl.Monitor
	stopWait time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

// newDNSBLRuntime loads and validates the config at path. It returns nil and
// no error when the monitor is off, because path is empty or the file says
// "enabled": false; then newResolver is never called and nothing is sent. A
// file that is set and does not load, or does not validate, is an error.
func newDNSBLRuntime(path string, newResolver func() dnsbl.Resolver, obs dnsbl.Observer) (*dnsblRuntime, error) {
	if path == "" {
		return nil, nil
	}
	cfg, enabled, err := dnsbl.LoadConfigFile(path)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	checker, err := dnsbl.NewChecker(newResolver(), cfg.Timeout)
	if err != nil {
		return nil, err
	}
	cfg.Observer = obs
	m, err := dnsbl.NewMonitor(checker, cfg)
	if err != nil {
		return nil, err
	}
	providers := make([]string, len(cfg.Providers))
	for i, p := range cfg.Providers {
		providers[i] = p.Name
	}
	slog.Info("DNSBL monitor enabled",
		"ips", len(cfg.IPs),
		"providers", providers,
		"start_delay", cfg.StartDelay.String(),
		"interval", cfg.Interval.String(),
		"timeout", cfg.Timeout.String(),
	)
	return &dnsblRuntime{monitor: m, cfg: cfg, checker: checker, stopWait: cfg.Timeout + dnsblStopGrace}, nil
}

// prepare must succeed after migration and before start. A failed seed must
// stop startup: treating unreadable history as unknown would repeat listings
// and miss recoveries. With monitoring disabled there is no database access.
func (r *dnsblRuntime) prepare(database *gorm.DB) error {
	if r == nil {
		return nil
	}
	obs := dnsbl.NewEventObserver(database, r.cfg.Observer)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	known, err := obs.Known(ctx, r.cfg)
	if err != nil {
		return err
	}
	cfg := r.cfg
	cfg.Observer, cfg.Known = obs, known
	m, err := dnsbl.NewMonitor(r.checker, cfg)
	if err != nil {
		return err
	}
	r.monitor = m
	return nil
}

// start runs the monitor on its own goroutine until stop or parent is done.
func (r *dnsblRuntime) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		if err := r.monitor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("DNSBL monitor stopped", "error", err)
		}
	}()
}

// stop cancels the monitor and waits for it to return, at most stopWait.
func (r *dnsblRuntime) stop() error {
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-time.After(r.stopWait):
		return fmt.Errorf("DNSBL monitor did not stop within %s", r.stopWait)
	}
}
