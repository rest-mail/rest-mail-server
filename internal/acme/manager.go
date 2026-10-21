package acme

import (
	"context"
	"log/slog"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

const (
	// DefaultCheckInterval is how often the manager checks for certificates
	// needing renewal.
	DefaultCheckInterval = 12 * time.Hour

	// renewalTimeout is the maximum time allowed for a single certificate renewal.
	renewalTimeout = 5 * time.Minute
)

// renewer is the part of Client the manager drives, so tests can stand in for
// the database and the CA.
type renewer interface {
	CertificatesNeedingRenewal() ([]models.Certificate, error)
	RenewCertificate(ctx context.Context, cert *models.Certificate) error
}

// Manager periodically checks for certificates that need renewal and renews them.
type Manager struct {
	client   renewer
	interval time.Duration
	delay    time.Duration // before the first check, so the server can start
	ctx      context.Context
	cancel   context.CancelFunc
	started  bool
	done     chan struct{}
}

// NewManager creates a new certificate renewal manager.
func NewManager(client *Client, interval time.Duration) *Manager {
	return newManager(client, interval)
}

func newManager(client renewer, interval time.Duration) *Manager {
	if interval == 0 {
		interval = DefaultCheckInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		client:   client,
		interval: interval,
		delay:    time.Minute,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

// Start begins the periodic renewal check loop in a background goroutine.
func (m *Manager) Start() {
	m.started = true
	go m.run()
	slog.Info("acme: renewal manager started", "interval", m.interval)
}

// Shutdown stops the renewal manager. It cancels a renewal in flight, skips
// the certificates not yet reached, and returns once the loop has exited, so
// no CA order or certificate write outlives the server.
func (m *Manager) Shutdown() {
	m.cancel()
	if m.started {
		<-m.done
	}
	slog.Info("acme: renewal manager stopped")
}

func (m *Manager) run() {
	defer close(m.done)
	timer := time.NewTimer(m.delay)
	defer timer.Stop()

	for {
		select {
		case <-m.ctx.Done():
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
		if m.ctx.Err() != nil {
			return
		}
		cert := &certs[i]
		ctx, cancel := context.WithTimeout(m.ctx, renewalTimeout)

		err := m.client.RenewCertificate(ctx, cert)
		switch {
		case err != nil && m.ctx.Err() != nil:
			slog.Info("acme: certificate renewal interrupted by shutdown",
				"domain", cert.Domain.Name,
				"cert_id", cert.ID,
			)
		case err != nil:
			slog.Error("acme: certificate renewal failed",
				"domain", cert.Domain.Name,
				"cert_id", cert.ID,
				"expires", cert.NotAfter,
				"error", err,
			)
		default:
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
