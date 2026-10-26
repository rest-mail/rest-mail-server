package dkimrotate

import (
	"context"
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

// maxSelectorTries bounds the search for a selector no stored key uses. Only
// the current and previous selectors can be taken, so three always suffice.
const maxSelectorTries = 3

// Stage makes the key that will replace domainID's current one, if rotation
// is due under the domain's saved interval, and saves it in the staged slot.
// It does not publish the key or change the signing key: the current key
// stays on the domain until a later step activates the staged one.
//
// It returns the staged key, without its private key, and whether this call
// made it. A domain that
// already has a staged key keeps it (returned with false), so a worker that
// stops between staging and publishing resumes with the same key. A domain
// with no current key, rotation off, or a key not yet due returns nil.
//
// The key is checked with outbound.CheckDKIMKey before it is saved, and is
// encrypted with masterKey as the current key is. ttl is the overlap the later
// steps wait after publishing it.
func Stage(ctx context.Context, database *gorm.DB, domainID uint, masterKey string, ttl time.Duration, now time.Time) (*models.DKIMRotationKey, bool, error) {
	if ttl < time.Second || ttl > (1<<31-1)*time.Second {
		return nil, false, fmt.Errorf("dkim rotation: TTL %v is outside 1s to 2^31-1s", ttl)
	}
	var staged *models.DKIMRotationKey
	var made bool
	// Silence SQL: an INSERT error can otherwise include ciphertext in GORM's
	// diagnostic query.
	database = database.Session(&gorm.Session{Logger: logger.Discard}).WithContext(ctx)
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL lock_timeout = '3s'").Error; err != nil {
			return err
		}
		// Locking the domain serialises staging with key changes made through
		// the API, so the selector chosen here cannot be taken meanwhile.
		var domain models.Domain
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "name", "dkim_selector", "dkim_private_key", "dkim_created_at", "dkim_rotation_interval_days").
			Take(&domain, domainID).Error; err != nil {
			return err
		}
		if domain.DKIMSelector == "" || domain.DKIMPrivateKey == "" {
			return nil
		}
		var slots []models.DKIMRotationKey
		if err := tx.Select("id", "domain_id", "slot", "selector", "installed_at", "published_at", "ttl_seconds", "retire_after", "created_at", "updated_at").
			Where("domain_id = ?", domain.ID).Find(&slots).Error; err != nil {
			return err
		}
		taken := map[string]bool{strings.ToLower(domain.DKIMSelector): true}
		for i := range slots {
			if slots[i].Slot == models.DKIMRotationStaged {
				staged = &slots[i]
				return nil
			}
			taken[strings.ToLower(slots[i].Selector)] = true
		}
		interval := time.Duration(domain.DKIMRotationIntervalDays) * 24 * time.Hour
		if !Due(domain.DKIMCreatedAt, interval, now) {
			return nil
		}

		selector := NextSelector(domain.DKIMSelector)
		for i := 1; taken[strings.ToLower(selector)]; i++ {
			if i == maxSelectorTries {
				return fmt.Errorf("dkim rotation: no free selector after %s", domain.DKIMSelector)
			}
			selector = NextSelector(selector)
		}
		private, _, err := dkim.GenerateKey(outbound.MinDKIMRSABits)
		if err != nil {
			return err
		}
		if err := outbound.CheckDKIMKey(domain.Name, selector, private); err != nil {
			return err
		}
		key := models.DKIMRotationKey{
			DomainID:   domain.ID,
			Slot:       models.DKIMRotationStaged,
			Selector:   selector,
			TTLSeconds: int64(ttl / time.Second),
		}
		if err := key.SetPrivateKey(private, masterKey); err != nil {
			return err
		}
		if err := tx.Omit("Domain").Create(&key).Error; err != nil {
			return err
		}
		key.PrivateKey = ""
		staged, made = &key, true
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("dkim rotation: staging a key for domain %d: %w", domainID, err)
	}
	return staged, made, nil
}
