package dkimrotate

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// ErrPreviousKeyRetained means activation would overwrite the rollback key.
// Retirement must explicitly remove that slot before another activation.
var ErrPreviousKeyRetained = errors.New("dkim rotation: previous key is still retained")

// ErrSelectorConflict means an operator has installed the staged selector as
// the current selector. Reconcile it before proceeding; its DNS record may be
// in use by the current key and must not be overwritten or removed.
var ErrSelectorConflict = errors.New("dkim rotation: staged selector is already current")

// Activate atomically moves the staged key to the domain and retains the old
// signing key in the previous slot. It returns true only after committing.
// Nothing changes until publication is recorded, one staged TTL has elapsed,
// and lookup (the caller's own resolver) sees the staged public key.
//
// No staged key, disabled rotation, a current key no longer due, or an unelapsed
// publication TTL returns false, nil. A missing/mismatched DNS record returns
// ErrNotPublished. Existing previous keys and selector conflicts require
// reconciliation and return the corresponding sentinel error. Both keys must
// decrypt and pass the outbound signability check before any writes occur.
//
// The previous key keeps its original installation time and ciphertext, with
// RetireAfter set to the switch time plus the staged TTL. Pass time.Now as clock;
// it is read again after DNS verification so a slow lookup cannot consume the
// retention overlap. This function never alters DNS,
// retires keys, or runs automatically; a worker must supply a bounded context.
func Activate(ctx context.Context, database *gorm.DB, domainID uint, masterKey string, lookup LookupTXT, clock func() time.Time) (bool, error) {
	if clock == nil {
		return false, fmt.Errorf("dkim rotation: activation requires a clock")
	}
	activated := false
	// Failed SQL must never log key material, including plaintext in dev mode.
	database = database.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx)
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL lock_timeout = '3s'").Error; err != nil {
			return err
		}
		var domain models.Domain
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&domain, domainID).Error; err != nil {
			return err
		}
		now := clock()
		if domain.DKIMSelector == "" || domain.DKIMPrivateKey == "" ||
			!Due(domain.DKIMCreatedAt, time.Duration(domain.DKIMRotationIntervalDays)*24*time.Hour, now) {
			return nil
		}
		var staged models.DKIMRotationKey
		err := tx.Where("domain_id = ? AND slot = ?", domainID, models.DKIMRotationStaged).Take(&staged).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.EqualFold(domain.DKIMSelector, staged.Selector) {
			return ErrSelectorConflict
		}
		if staged.PublishedAt == nil || now.Sub(*staged.PublishedAt) < time.Duration(staged.TTLSeconds)*time.Second {
			return nil
		}
		var previousCount int64
		if err := tx.Model(&models.DKIMRotationKey{}).
			Where("domain_id = ? AND slot = ?", domainID, models.DKIMRotationPrevious).Count(&previousCount).Error; err != nil {
			return err
		}
		if previousCount != 0 {
			return ErrPreviousKeyRetained
		}
		current, err := models.LoadDKIMPrivateKey(domain.DKIMPrivateKey, masterKey)
		if err != nil {
			return err
		}
		if err := outbound.CheckDKIMKey(domain.Name, domain.DKIMSelector, current); err != nil {
			return err
		}
		plain, err := models.LoadDKIMPrivateKey(staged.PrivateKey, masterKey)
		if err != nil {
			return err
		}
		if err := outbound.CheckDKIMKey(domain.Name, staged.Selector, plain); err != nil {
			return err
		}
		signer, err := outbound.ParseDKIMKey(plain)
		if err != nil {
			return err
		}
		public, err := x509.MarshalPKIXPublicKey(signer.Public())
		if err != nil {
			return err
		}
		record, err := dkim.RecordValue(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public})))
		if err != nil {
			return err
		}
		if lookup == nil {
			return fmt.Errorf("dkim rotation: activation requires a DNS resolver")
		}
		if err := Published(ctx, lookup, domain.Name, staged.Selector, record); err != nil {
			return err
		}
		switchedAt := clock()
		retireAfter := switchedAt.Add(time.Duration(staged.TTLSeconds) * time.Second)
		previous := models.DKIMRotationKey{
			DomainID: domainID, Slot: models.DKIMRotationPrevious,
			Selector: domain.DKIMSelector, PrivateKey: domain.DKIMPrivateKey,
			InstalledAt: domain.DKIMCreatedAt, TTLSeconds: staged.TTLSeconds, RetireAfter: &retireAfter,
		}
		if err := tx.Omit("Domain").Create(&previous).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain).Updates(map[string]any{
			"dkim_selector": staged.Selector, "dkim_private_key": staged.PrivateKey, "dkim_created_at": switchedAt,
		}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&staged).Error; err != nil {
			return err
		}
		activated = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("dkim rotation: activating a key for domain %d: %w", domainID, err)
	}
	return activated, nil
}
