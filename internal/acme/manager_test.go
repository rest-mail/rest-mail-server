package acme

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// blockingRenewer reports two certificates due and blocks each renewal until
// its context ends, recording which certificates it was asked to renew.
type blockingRenewer struct {
	mu      sync.Mutex
	renewed []uint
	entered chan struct{}
}

func (r *blockingRenewer) CertificatesNeedingRenewal() ([]models.Certificate, error) {
	return []models.Certificate{{ID: 1}, {ID: 2}}, nil
}

func (r *blockingRenewer) RenewCertificate(ctx context.Context, cert *models.Certificate) error {
	r.mu.Lock()
	r.renewed = append(r.renewed, cert.ID)
	r.mu.Unlock()
	r.entered <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

func TestManagerShutdownCancelsRenewalInFlight(t *testing.T) {
	r := &blockingRenewer{entered: make(chan struct{}, 2)}
	m := newManager(r, time.Hour)
	m.delay = 0
	m.Start()

	select {
	case <-r.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("renewal never started")
	}

	stopped := make(chan struct{})
	go func() {
		m.Shutdown()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not cancel the renewal in flight")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.renewed) != 1 || r.renewed[0] != 1 {
		t.Fatalf("renewed %v after shutdown, want only the certificate in flight [1]", r.renewed)
	}
}

func TestManagerShutdownBeforeFirstCheck(t *testing.T) {
	r := &blockingRenewer{entered: make(chan struct{}, 2)}
	m := newManager(r, time.Hour)
	m.Start()

	stopped := make(chan struct{})
	go func() {
		m.Shutdown()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown waited for the first check")
	}
	if len(r.renewed) != 0 {
		t.Fatalf("renewed %v, want nothing before the first check", r.renewed)
	}
}

func TestManagerShutdownWithoutStart(t *testing.T) {
	m := newManager(&blockingRenewer{}, 0)
	if m.interval != DefaultCheckInterval {
		t.Fatalf("interval %v, want default %v", m.interval, DefaultCheckInterval)
	}
	m.Shutdown() // must not block on a loop that never ran
}
