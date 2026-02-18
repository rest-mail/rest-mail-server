package acme

import (
	"context"
	"log/slog"
	"time"
)

const (
	// DefaultCheckInterval is how often the manager checks for certificates
	// needing renewal.
	DefaultCheckInterval = 12 * time.Hour

	// renewalTimeout is the maximum time allowed for a single certificate renewal.
	renewalTimeout = 5 * time.Minute
)

// Manager periodically checks for certificates that need renewal and renews them.
type Manager struct {
	client   *Client
	interval time.Duration
	stop     chan struct{}
}

// NewManager creates a new certificate renewal manager.
func NewManager(client *Client, interval time.Duration) *Manager {
	if interval == 0 {
		interval = DefaultCheckInterval
	}
	return &Manager{
		client:   client,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start begins the periodic renewal check loop in a background goroutine.
func (m *Manager) Start() {
	go m.run()
	slog.Info("acme: renewal manager started", "interval", m.interval)
}

// Shutdown stops the renewal manager.
func (m *Manager) Shutdown() {
	close(m.stop)
	slog.Info("acme: renewal manager stopped")
}

func (m *Manager) run() {
	// Run an initial check after a short delay to allow the server to start.
	timer := time.NewTimer(1 * time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-m.stop:
			return
		case <-timer.C:
			m.checkAndRenew()
			timer.Reset(m.interval)
		}
	}
}

// checkAndRenew finds certificates needing renewal and renews them.
func (m *Manager) checkAndRenew() {
	certs, err := m.client.CertificatesNeedingRenewal()
	if err != nil {
		slog.Error("acme: failed to check certificates for renewal", "error", err)
		return
	}

	if len(certs) == 0 {
		slog.Debug("acme: no certificates need renewal")
		return
	}

	slog.Info("acme: certificates needing renewal", "count", len(certs))

	for i := range certs {
		cert := &certs[i]
		ctx, cancel := context.WithTimeout(context.Background(), renewalTimeout)

		if err := m.client.RenewCertificate(ctx, cert); err != nil {
			slog.Error("acme: certificate renewal failed",
				"domain", cert.Domain.Name,
				"cert_id", cert.ID,
				"expires", cert.NotAfter,
				"error", err,
			)
		} else {
			slog.Info("acme: certificate renewal succeeded",
				"domain", cert.Domain.Name,
				"cert_id", cert.ID,
			)
		}

		cancel()
	}
}

// CheckNow triggers an immediate check for certificates needing renewal.
// This is useful for testing or manual trigger via an admin API.
func (m *Manager) CheckNow() {
	m.checkAndRenew()
}
