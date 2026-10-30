package repositories

import (
	"errors"
	"fmt"
	"math"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrStorageQuota = errors.New("mailbox storage quota exceeded")

// StorageQuotaChain returns this mailbox followed by its owners. When locking,
// callers must use a transaction: root-first row locks serialize reservations
// by an owner and all descendants, including concurrent sibling appends.
func StorageQuotaChain(db *gorm.DB, id uint, lock bool) ([]models.Mailbox, error) {
	var chain []models.Mailbox
	seen := map[uint]bool{}
	for {
		if seen[id] {
			return nil, errors.New("cyclic mailbox ownership")
		}
		seen[id] = true
		var mb models.Mailbox
		if err := db.First(&mb, id).Error; err != nil {
			return nil, err
		}
		chain = append(chain, mb)
		if mb.OwnerID == nil {
			break
		}
		id = *mb.OwnerID
	}
	if lock {
		for i := len(chain) - 1; i >= 0; i-- {
			var mb models.Mailbox
			if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&mb, chain[i].ID).Error; err != nil {
				return nil, err
			}
			if (mb.OwnerID == nil) != (chain[i].OwnerID == nil) || (mb.OwnerID != nil && *mb.OwnerID != *chain[i].OwnerID) {
				return nil, errors.New("mailbox ownership changed during quota check")
			}
			chain[i] = mb
		}
	}
	return chain, nil
}

// StorageQuotaCeiling is the live ceiling, with zero meaning unlimited. An
// owned mailbox's zero cap inherits; a positive cap can only narrow its owners.
func StorageQuotaCeiling(chain []models.Mailbox) int64 {
	var ceiling int64
	for _, mb := range chain {
		if mb.QuotaBytes > 0 && (ceiling == 0 || mb.QuotaBytes < ceiling) {
			ceiling = mb.QuotaBytes
		}
	}
	return ceiling
}

// StorageRemaining includes all descendants, even inactive accounts and those
// in other domains: stored mail continues to consume the owner's allowance.
// Every existing mailbox/domain/server admission check still runs separately.
func StorageRemaining(db *gorm.DB, chain []models.Mailbox) (int64, error) {
	remaining := int64(math.MaxInt64)
	for _, mb := range chain {
		if mb.QuotaBytes <= 0 {
			continue
		}
		var used int64
		if err := db.Raw(`WITH RECURSIVE owned(id, quota_used_bytes) AS (
			SELECT id, quota_used_bytes FROM mailboxes WHERE id = ?
			UNION
			SELECT m.id, m.quota_used_bytes FROM mailboxes m JOIN owned o ON m.owner_id = o.id
		) SELECT COALESCE(SUM(quota_used_bytes), 0) FROM owned`, mb.ID).Scan(&used).Error; err != nil {
			return 0, err
		}
		if left := mb.QuotaBytes - used; left < remaining {
			remaining = left
		}
	}
	return remaining, nil
}

// ReserveStorage must run in the message's transaction, before quota_usage or
// message writes. A failed insert or charge rolls this reservation back too.
func ReserveStorage(tx *gorm.DB, id uint, cost int64) error {
	if cost < 0 {
		return fmt.Errorf("negative storage charge: %d", cost)
	}
	chain, err := StorageQuotaChain(tx, id, true)
	if err != nil {
		return err
	}
	remaining, err := StorageRemaining(tx, chain)
	if err != nil {
		return err
	}
	if cost > remaining {
		return ErrStorageQuota
	}
	return tx.Model(&models.Mailbox{}).Where("id = ?", id).Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", cost)).Error
}
