package bootstrap

import (
	"errors"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// Recovery for a running server, done by whoever controls the deployment: the
// operator commands `restmail-api reset-password` and `restmail-api
// revoke-sessions`, which `chore password:reset` and `chore sessions:revoke`
// run in a pod of the API's own. Neither needs mail, DNS, a certificate or a
// working sign-in — the recovery for a mail server cannot depend on the thing
// that is broken.

// ResetPassword gives an admin a new password, chosen by whoever ran the reset,
// and puts the account back in setup: the password must be replaced at the
// next sign-in and an authenticator enrolled afresh, because the case this
// exists for is an admin who has lost their way in — which may be the
// authenticator as much as the password.
//
// Every session the account had ends. The refresh tokens are revoked as well
// as the epoch bumped, because a refresh re-mints at the current epoch rather
// than checking the presented one.
//
// The bootstrap expiry is cleared, so the deployment's own password can never
// re-arm the account afterwards: a restart must not quietly replace the
// password someone has just been handed.
func ResetPassword(database *gorm.DB, username, password string) (*models.AdminUser, error) {
	if len(password) < MinAdminPasswordLength {
		return nil, ErrResetPasswordTooShort
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	var admin models.AdminUser
	err = database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("username = ?", username).First(&admin).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNoSuchAdmin
			}
			return err
		}
		// Refresh tokens first: a dead row can mint nothing, whatever happens
		// to the epoch after it.
		if err := repositories.NewRefreshTokenRepository(tx).RevokeAllForSubject("admin", admin.ID); err != nil {
			return err
		}
		if err := tx.Model(&admin).Updates(map[string]any{
			"password_hash":            hash,
			"password_change_required": true,
			"two_factor_required":      true,
			"bootstrap_expires_at":     nil,
			"active":                   true,
			"credential_epoch":         gorm.Expr("credential_epoch + 1"),
		}).Error; err != nil {
			return err
		}
		if err := repositories.NewTwoFactorRepository(tx).Delete(models.TwoFactorUserTypeAdmin, admin.ID); err != nil {
			return err
		}
		return tx.First(&admin, admin.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &admin, nil
}

// ErrResetPasswordTooShort: a reset password is generated, not remembered, so
// it is held to the bootstrap password's length rather than a chosen one's.
var ErrResetPasswordTooShort = errors.New("a reset password must be at least 16 characters")

// SessionsRevoked is what RevokeAllSessions ended.
type SessionsRevoked struct {
	RefreshTokens int64 `json:"refresh_tokens"`
	Admins        int64 `json:"admins"`
}

// RevokeAllSessions signs everybody out: every refresh token, every admin's
// access tokens.
//
// The order is the point. Refresh tokens go first, so nothing can be minted
// behind the sweep; bumping the admins' epochs first would send each admin's
// client to refresh, and the refresh would re-mint at the new epoch and let
// them straight back in.
//
// A mailbox user's access token carries no epoch and stays valid until it
// expires, which is minutes, not days. Run with the API stopped
// (`chore server:offline`) and nothing can refresh in the meantime either.
func RevokeAllSessions(database *gorm.DB) (SessionsRevoked, error) {
	var out SessionsRevoked
	err := database.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.RefreshToken{}).
			Where("status = ?", models.RefreshTokenActive).
			Updates(map[string]any{"status": models.RefreshTokenRevoked, "updated_at": time.Now()})
		if res.Error != nil {
			return res.Error
		}
		out.RefreshTokens = res.RowsAffected
		res = tx.Model(&models.AdminUser{}).
			Where("1 = 1").
			Update("credential_epoch", gorm.Expr("credential_epoch + 1"))
		if res.Error != nil {
			return res.Error
		}
		out.Admins = res.RowsAffected
		return nil
	})
	return out, err
}
