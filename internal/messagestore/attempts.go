package messagestore

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AttemptLease allows several missed heartbeat ticks before a crashed
// worker's reference is recovered.
const AttemptLease = 5 * time.Minute

// PinAttempt records the owner and increments the count in the claim transaction.
func (s *Sources) PinAttempt(ctx context.Context, tx *gorm.DB, ref string) (string, error) {
	if err := s.AddReference(ctx, tx, ref); err != nil {
		return "", err
	}
	pin := models.MessageSourceAttempt{ID: rand.Text(), SourceRef: ref, ExpiresAt: time.Now().Add(AttemptLease)}
	return pin.ID, tx.WithContext(ctx).Create(&pin).Error
}

// RenewAttempt extends the pin independently of the queue claim. A superseded
// worker can still finish delivering, so it needs to keep its own source alive.
func RenewAttempt(ctx context.Context, db *gorm.DB, id string) error {
	res := db.WithContext(ctx).Model(&models.MessageSourceAttempt{}).
		Where("id = ? AND expires_at > clock_timestamp()", id).
		UpdateColumn("expires_at", time.Now().Add(AttemptLease))
	return sourceUpdate(res)
}

// UnpinAttempt is idempotent even when a previous deletion's commit was ambiguous.
func UnpinAttempt(ctx context.Context, db *gorm.DB, id string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var pin []models.MessageSourceAttempt
		if err := tx.Clauses(clause.Returning{}).Where("id = ?", id).Delete(&pin).Error; err != nil {
			return err
		}
		for _, owner := range pin {
			if err := ReleaseReference(ctx, tx, owner.SourceRef); err != nil {
				return err
			}
		}
		return nil
	})
}

func reapAttempts(ctx context.Context, db *gorm.DB) error {
	for range reapBatch {
		removed := false
		err := db.Transaction(func(tx *gorm.DB) error {
			var pin models.MessageSourceAttempt
			result := tx.Where("expires_at <= clock_timestamp()").Order("expires_at").
				Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Limit(1).Find(&pin)
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
			if err := tx.Delete(&pin).Error; err != nil {
				return err
			}
			removed = true
			return ReleaseReference(ctx, tx, pin.SourceRef)
		})
		if err != nil || !removed {
			return err
		}
	}
	return nil
}
