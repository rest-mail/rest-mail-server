package dkimrotate

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// RecordTTL is the TTL a DKIM record is published with (dns.MailRecords) and so the
// overlap a rotation waits: after the new record is seen, before signing with it, and after
// the switch, before the old key is let go.
const RecordTTL = time.Hour

// stepTimeout bounds one domain's step: its DNS lookup and its transactions.
const stepTimeout = time.Minute

// Worker runs rotations for every domain with an interval (#728). It never writes DNS: the
// staged key's record goes out with the operator's records (restmail-api dns), and the worker
// moves on once the record resolves, as dkimrotate.Published sees it.
//
// Each tick takes every domain one step:
//  1. a domain whose key is due gets a staged key (Stage);
//  2. a staged key whose record now resolves has its publication recorded;
//  3. one TTL after publication the staged key becomes the signing key (Activate), the old
//     one kept as previous;
//  4. the previous key is let go once its retirement deadline has passed, and its record
//     leaves the operator's records.
type Worker struct {
	db        *gorm.DB
	masterKey string
	lookup    LookupTXT
	interval  time.Duration
	now       func() time.Time

	stop chan struct{}
	wg   sync.WaitGroup
}

// NewWorker builds a rotation worker ticking every interval (production: 15 minutes),
// resolving records with lookup.
func NewWorker(db *gorm.DB, masterKey string, lookup LookupTXT, interval time.Duration) *Worker {
	return &Worker{db: db, masterKey: masterKey, lookup: lookup, interval: interval, now: time.Now, stop: make(chan struct{})}
}

// Start launches the rotation loop.
func (w *Worker) Start() {
	w.wg.Add(1)
	go w.loop()
	slog.Info("dkim rotation worker started", "interval", w.interval)
}

// Shutdown stops the rotation loop.
func (w *Worker) Shutdown() {
	close(w.stop)
	w.wg.Wait()
	slog.Info("dkim rotation worker stopped")
}

func (w *Worker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.Tick(context.Background())
		}
	}
}

// Tick takes every domain with rotation on, or with a rotation under way, one step. A domain
// that fails is logged and left for the next tick; the others go on.
func (w *Worker) Tick(ctx context.Context) {
	var domainIDs []uint
	err := w.db.WithContext(ctx).Model(&models.Domain{}).
		Where("dkim_rotation_interval_days > 0 OR id IN (?)",
			w.db.Model(&models.DKIMRotationKey{}).Select("domain_id")).
		Order("id").Pluck("id", &domainIDs).Error
	if err != nil {
		slog.Error("dkim rotation: listing domains failed", "error", err)
		return
	}
	for _, id := range domainIDs {
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
		if err := w.Step(stepCtx, id); err != nil {
			slog.Warn("dkim rotation: a step failed; the next tick retries", "domain_id", id, "error", err)
		}
		cancel()
	}
}

// Step takes one domain one step of its rotation.
func (w *Worker) Step(ctx context.Context, domainID uint) error {
	if err := w.retire(ctx, domainID); err != nil {
		return err
	}
	staged, made, err := Stage(ctx, w.db, domainID, w.masterKey, RecordTTL, w.now())
	if err != nil {
		return err
	}
	if staged == nil {
		return nil
	}
	if made {
		slog.Info("dkim rotation: a new key is staged; publish its record", "domain_id", domainID, "selector", staged.Selector)
		return nil
	}
	if staged.PublishedAt == nil {
		return w.notePublication(ctx, domainID, staged.ID)
	}
	activated, err := Activate(ctx, w.db, domainID, w.masterKey, w.lookup, w.now)
	if errors.Is(err, ErrPreviousKeyRetained) {
		return nil // the previous key retires first, on a later tick
	}
	if err != nil {
		return err
	}
	if activated {
		slog.Info("dkim rotation: now signing with the new key", "domain_id", domainID, "selector", staged.Selector)
	}
	return nil
}

// notePublication records when the staged key's record was first seen resolving, which
// starts its TTL. A record not there yet is waited for.
func (w *Worker) notePublication(ctx context.Context, domainID, stagedID uint) error {
	database := w.db.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx)
	var staged models.DKIMRotationKey
	if err := database.Take(&staged, stagedID).Error; err != nil {
		return err
	}
	var domain models.Domain
	if err := database.Select("id", "name").Take(&domain, domainID).Error; err != nil {
		return err
	}
	record, err := PublicRecord(staged.PrivateKey, w.masterKey)
	if err != nil {
		return err
	}
	if err := Published(ctx, w.lookup, domain.Name, staged.Selector, record); err != nil {
		if errors.Is(err, ErrNotPublished) {
			return nil // not there yet: the record is in the operator's records to publish
		}
		return err
	}
	now := w.now()
	result := database.Model(&models.DKIMRotationKey{}).
		Where("id = ? AND slot = ? AND published_at IS NULL", staged.ID, models.DKIMRotationStaged).
		Update("published_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		slog.Info("dkim rotation: the new key's record resolves; switching after one TTL", "domain_id", domainID, "selector", staged.Selector)
	}
	return nil
}

// retire lets the previous key go once its retirement deadline has passed: mail it signed
// before the switch has had one TTL to be verified.
func (w *Worker) retire(ctx context.Context, domainID uint) error {
	result := w.db.WithContext(ctx).
		Where("domain_id = ? AND slot = ? AND retire_after <= ?", domainID, models.DKIMRotationPrevious, w.now()).
		Delete(&models.DKIMRotationKey{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		slog.Info("dkim rotation: the previous key is retired; its record can go", "domain_id", domainID)
	}
	return nil
}

// PublicRecord is the DKIM record value (RFC 6376 §3.6.1) that publishes the public half of
// a stored private key.
func PublicRecord(stored, masterKey string) (string, error) {
	keyPEM, err := models.LoadDKIMPrivateKey(stored, masterKey)
	if err != nil {
		return "", err
	}
	priv, err := outbound.ParseDKIMKey(keyPEM)
	if err != nil {
		return "", fmt.Errorf("the key %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(priv.Public())
	if err != nil {
		return "", err
	}
	return dkim.RecordValue(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})))
}
