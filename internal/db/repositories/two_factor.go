package repositories

import (
	"errors"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// ErrTwoFactorNotFound is returned when an account has no matching 2FA
// enrollment row. Callers treat it as "2FA not set up" (login proceeds without
// a code) rather than an error.
var ErrTwoFactorNotFound = errors.New("two-factor enrollment not found")

// TwoFactorRepository is the gorm-backed store for optional TOTP 2FA
// enrollments (OSI-19). It records one enrollment per owner (keyed by
// UserType+SubjectID) plus that enrollment's one-time recovery codes, and
// exposes the enroll → confirm → verify → disable lifecycle the auth handler
// drives.
type TwoFactorRepository struct {
	db *gorm.DB
}

func NewTwoFactorRepository(db *gorm.DB) *TwoFactorRepository {
	return &TwoFactorRepository{db: db}
}

// Get returns the enrollment row for an owner (confirmed or pending), or
// ErrTwoFactorNotFound when none exists.
func (r *TwoFactorRepository) Get(userType string, subjectID uint) (*models.TwoFactor, error) {
	var tf models.TwoFactor
	err := r.db.Where("user_type = ? AND subject_id = ?", userType, subjectID).First(&tf).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTwoFactorNotFound
	}
	if err != nil {
		return nil, err
	}
	return &tf, nil
}

// GetActive returns the enrollment only when it is confirmed (i.e. 2FA is
// actually active for the account). A pending/unconfirmed enrollment yields
// ErrTwoFactorNotFound so it never gates login — this is what keeps a
// half-finished enrollment from locking anyone out.
func (r *TwoFactorRepository) GetActive(userType string, subjectID uint) (*models.TwoFactor, error) {
	var tf models.TwoFactor
	err := r.db.Where("user_type = ? AND subject_id = ? AND confirmed = ?", userType, subjectID, true).First(&tf).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTwoFactorNotFound
	}
	if err != nil {
		return nil, err
	}
	return &tf, nil
}

// Enroll creates (or replaces) a PENDING enrollment for an owner: it wipes any
// prior enrollment for that owner and its recovery codes, then inserts a fresh
// unconfirmed row carrying the encrypted secret plus the hashed recovery codes.
// Replacing in one transaction means a re-enroll can't leave stale codes or a
// dangling secret behind. The returned row is unconfirmed until Confirm runs.
func (r *TwoFactorRepository) Enroll(userType string, subjectID uint, encryptedSecret string, recoveryHashes []string) (*models.TwoFactor, error) {
	tf := &models.TwoFactor{
		UserType:        userType,
		SubjectID:       subjectID,
		EncryptedSecret: encryptedSecret,
		Confirmed:       false,
	}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := r.deleteForOwnerTx(tx, userType, subjectID); err != nil {
			return err
		}
		if err := tx.Create(tf).Error; err != nil {
			return err
		}
		for _, h := range recoveryHashes {
			if err := tx.Create(&models.TwoFactorRecoveryCode{
				TwoFactorID: tf.ID,
				CodeHash:    h,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tf, nil
}

// Confirm marks a pending enrollment active. The status guard (confirmed=false)
// makes it idempotent-safe and prevents a stale confirm from resurrecting a row.
// Returns ErrTwoFactorNotFound when there is no pending row to confirm.
func (r *TwoFactorRepository) Confirm(id uint) error {
	now := time.Now()
	res := r.db.Model(&models.TwoFactor{}).
		Where("id = ? AND confirmed = ?", id, false).
		Updates(map[string]any{"confirmed": true, "confirmed_at": now, "updated_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrTwoFactorNotFound
	}
	return nil
}

// Delete removes an owner's enrollment and all its recovery codes (2FA disable
// / re-enroll). Idempotent: deleting a non-existent enrollment is not an error.
func (r *TwoFactorRepository) Delete(userType string, subjectID uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		return r.deleteForOwnerTx(tx, userType, subjectID)
	})
}

// deleteForOwnerTx removes an owner's enrollment plus its recovery-code rows
// inside the supplied transaction. Recovery codes are deleted explicitly
// (AutoMigrate does not create an ON DELETE CASCADE) so no orphan hashes remain.
func (r *TwoFactorRepository) deleteForOwnerTx(tx *gorm.DB, userType string, subjectID uint) error {
	var existing []models.TwoFactor
	if err := tx.Where("user_type = ? AND subject_id = ?", userType, subjectID).Find(&existing).Error; err != nil {
		return err
	}
	for _, tf := range existing {
		if err := tx.Where("two_factor_id = ?", tf.ID).Delete(&models.TwoFactorRecoveryCode{}).Error; err != nil {
			return err
		}
	}
	return tx.Where("user_type = ? AND subject_id = ?", userType, subjectID).Delete(&models.TwoFactor{}).Error
}

// ConsumeRecoveryCode redeems a single-use recovery code for an enrollment. It
// bcrypt-compares the submitted code against each still-unused hash; on a match
// it stamps UsedAt under a used_at IS NULL guard, so a code is redeemable at
// most once even under a concurrent double-submit. Returns (true, nil) when a
// code was consumed, (false, nil) when none matched.
func (r *TwoFactorRepository) ConsumeRecoveryCode(twoFactorID uint, plaintext string) (bool, error) {
	if plaintext == "" {
		return false, nil
	}
	var codes []models.TwoFactorRecoveryCode
	if err := r.db.Where("two_factor_id = ? AND used_at IS NULL", twoFactorID).Find(&codes).Error; err != nil {
		return false, err
	}
	for i := range codes {
		if !auth.CheckRecoveryCode(plaintext, codes[i].CodeHash) {
			continue
		}
		now := time.Now()
		res := r.db.Model(&models.TwoFactorRecoveryCode{}).
			Where("id = ? AND used_at IS NULL", codes[i].ID).
			Update("used_at", now)
		if res.Error != nil {
			return false, res.Error
		}
		return res.RowsAffected == 1, nil
	}
	return false, nil
}
