package bootstrap

import (
	"context"
	"errors"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrAccountNotInvitable prevents an invitation from becoming a second way to
// reset an established account. Recovery remains a separate, stronger flow.
var ErrAccountNotInvitable = errors.New("only an active account that has never held credentials can be invited")

func invitationEligible(account models.Mailbox, credentials int64) bool {
	return account.Active && account.CredentialEpoch == 0 && account.LastPasswordChange == nil && credentials == 0
}

func invitationAccount(tx *gorm.DB, id uint) (models.Mailbox, error) {
	var account models.Mailbox
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, id).Error; err != nil {
		return account, err
	}
	var count int64
	if err := tx.Model(&models.Credential{}).Where("account_id = ?", id).Count(&count).Error; err != nil {
		return account, err
	}
	if !invitationEligible(account, count) {
		return account, ErrAccountNotInvitable
	}
	return account, nil
}

// MintInvitation hands out a short-lived, single-use token for an account made
// without a password. Reissuing invalidates its previous invitation. Only the
// token's hash is stored; transaction rollback preserves an older invitation if
// the replacement cannot be saved. The caller must enforce mailbox authority.
func MintInvitation(ctx context.Context, database *gorm.DB, accountID uint) (string, time.Time, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, err
	}
	var token string
	var expires time.Time
	err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := invitationAccount(tx, accountID); err != nil {
			return err
		}
		if err := tx.Model(&models.OneTimeToken{}).
			Where("account_id = ? AND purpose = ? AND used_at IS NULL", accountID, models.OneTimeTokenInvitation).
			Update("used_at", time.Now()).Error; err != nil {
			return err
		}
		var err error
		token, expires, err = mint(tx, models.OneTimeTokenInvitation, &accountID, DefaultTokenLifetime)
		return err
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// AcceptInvitation spends an invitation and installs the recipient's first
// password together. The new session must finish authenticator enrollment and
// save recovery codes before it can open mail. No grants are added or changed.
func AcceptInvitation(ctx context.Context, database *gorm.DB, token, password string) (*models.Mailbox, error) {
	if PasswordTooShort(password) {
		return nil, ErrPasswordTooShort
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	var account models.Mailbox
	err = database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidate models.OneTimeToken
		if err := tx.Where("token_hash = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?",
			hashToken(token), models.OneTimeTokenInvitation, time.Now()).First(&candidate).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTokenInvalid
			}
			return err
		}
		if candidate.AccountID == nil {
			return ErrTokenInvalid
		}
		// Issuance and redemption lock the account before changing tokens, in
		// the same order. Concurrent redemptions still use redeem's conditional
		// UPDATE, so exactly one can spend a token.
		var err error
		account, err = invitationAccount(tx, *candidate.AccountID)
		if errors.Is(err, ErrAccountNotInvitable) || errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if _, err := redeem(tx, token, models.OneTimeTokenInvitation); err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Model(&account).Updates(map[string]any{
			"password_change_required": false,
			"two_factor_required":      true,
			"bootstrap_expires_at":     nil,
			"last_password_change":     now,
			"credential_epoch":         gorm.Expr("credential_epoch + 1"),
		}).Error; err != nil {
			return err
		}
		if _, err := repositories.NewCredentialRepository(tx).SetPassword(account.ID, hash, &account.ID, now); err != nil {
			return err
		}
		return tx.First(&account, account.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &account, nil
}
