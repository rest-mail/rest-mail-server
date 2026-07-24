package repositories

import (
	"errors"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// ErrRefreshTokenNotFound is returned when a jti has no ledger row — an unknown,
// forged, or already-pruned refresh token. The refresh path treats it as a hard
// rejection (fail closed).
var ErrRefreshTokenNotFound = errors.New("refresh token not found")

// RefreshTokenRepository is the gorm-backed refresh-token rotation/revocation
// ledger. It records each issued refresh token by jti and lets the auth handler
// rotate one on use and revoke tokens out-of-band (logout / password change).
type RefreshTokenRepository struct {
	db *gorm.DB
}

func NewRefreshTokenRepository(db *gorm.DB) *RefreshTokenRepository {
	return &RefreshTokenRepository{db: db}
}

// Save inserts a new ledger row for a freshly issued refresh token (status
// active).
func (r *RefreshTokenRepository) Save(rec *models.RefreshToken) error {
	return r.db.Create(rec).Error
}

// GetByJTI looks up a ledger row by jti, returning ErrRefreshTokenNotFound when
// none exists.
func (r *RefreshTokenRepository) GetByJTI(jti string) (*models.RefreshToken, error) {
	var rec models.RefreshToken
	err := r.db.Where("jti = ?", jti).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRefreshTokenNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Rotate atomically moves an active token to rotated, refusing the update if it
// is not currently active. The rows-affected guard makes rotation single-use
// even under a concurrent double-refresh: exactly one caller flips active→rotated
// and the loser sees ErrRefreshTokenNotFound (already spent).
func (r *RefreshTokenRepository) Rotate(jti string) error {
	res := r.db.Model(&models.RefreshToken{}).
		Where("jti = ? AND status = ?", jti, models.RefreshTokenActive).
		Updates(map[string]any{"status": models.RefreshTokenRotated, "updated_at": time.Now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrRefreshTokenNotFound
	}
	return nil
}

// Revoke moves a single token to revoked by jti (logout). Idempotent: revoking
// an already-revoked or unknown jti is not an error, so a double logout is safe.
func (r *RefreshTokenRepository) Revoke(jti string) error {
	return r.db.Model(&models.RefreshToken{}).
		Where("jti = ?", jti).
		Updates(map[string]any{"status": models.RefreshTokenRevoked, "updated_at": time.Now()}).Error
}

// RevokeAllForSubject revokes every still-active refresh token for one owner
// (password change / account disable), killing all of that user's sessions at
// their next refresh.
func (r *RefreshTokenRepository) RevokeAllForSubject(userType string, subjectID uint) error {
	return r.db.Model(&models.RefreshToken{}).
		Where("user_type = ? AND subject_id = ? AND status = ?", userType, subjectID, models.RefreshTokenActive).
		Updates(map[string]any{"status": models.RefreshTokenRevoked, "updated_at": time.Now()}).Error
}
