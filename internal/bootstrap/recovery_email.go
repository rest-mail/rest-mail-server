package bootstrap

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrRecoveryEmailFrozen = errors.New("recovery address changes are frozen for three days after email recovery")

func lockRecoveryAccount(tx *gorm.DB, id uint) (*models.Mailbox, error) {
	var account models.Mailbox
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, id).Error; err != nil {
		return nil, err
	}
	if !account.Active || account.BootstrapExpired(time.Now()) {
		return nil, ErrTokenInvalid
	}
	return &account, nil
}

// RecoveryEmailExternal rejects every domain hosted here, including inactive
// domains. Recheck at issuance and redemption: domains can be added later.
func RecoveryEmailExternal(tx *gorm.DB, address string) (bool, error) {
	at := strings.LastIndex(address, "@")
	if at < 1 {
		return false, nil
	}
	var count int64
	err := tx.Model(&models.Domain{}).Where("LOWER(name) = ?", strings.ToLower(address[at+1:])).Count(&count).Error
	return count == 0, err
}

func invalidateEmailTokens(tx *gorm.DB, accountID uint) error {
	return tx.Model(&models.OneTimeToken{}).Where("account_id = ? AND purpose IN ? AND used_at IS NULL", accountID, []string{models.OneTimeTokenVerifyRecoveryEmail, models.OneTimeTokenRecoverEmail}).Update("used_at", time.Now()).Error
}

// RegisterRecoveryEmail creates a pending method and a verification token in
// tx. The caller queues delivery in the same transaction before committing.
func RegisterRecoveryEmail(ctx context.Context, tx *gorm.DB, id uint, address string) (string, time.Time, error) {
	tx = tx.WithContext(ctx)
	if _, err := lockRecoveryAccount(tx, id); err != nil {
		return "", time.Time{}, err
	}
	var method models.RecoveryEmail
	err := tx.First(&method, "account_id = ?", id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", time.Time{}, err
	}
	if method.FrozenUntil != nil && method.FrozenUntil.After(time.Now()) {
		return "", time.Time{}, ErrRecoveryEmailFrozen
	}
	if err := invalidateEmailTokens(tx, id); err != nil {
		return "", time.Time{}, err
	}
	method = models.RecoveryEmail{AccountID: id, Address: address}
	if err := tx.Save(&method).Error; err != nil {
		return "", time.Time{}, err
	}
	return mint(tx, models.OneTimeTokenVerifyRecoveryEmail, &id, DefaultTokenLifetime)
}

// RecoveryEmailTokenAccount locks the account before spending its token. It
// uses the same lock order as registration and issuance, and rejects tokens
// minted before any password change. Spending rolls back with the caller's tx.
func RecoveryEmailTokenAccount(ctx context.Context, tx *gorm.DB, token, purpose string) (*models.Mailbox, *models.RecoveryEmail, error) {
	tx = tx.WithContext(ctx)
	var candidate models.OneTimeToken
	if err := tx.Where("token_hash = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?", hashToken(token), purpose, time.Now()).First(&candidate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = ErrTokenInvalid
		}
		return nil, nil, err
	}
	if candidate.AccountID == nil {
		return nil, nil, ErrTokenInvalid
	}
	account, err := lockRecoveryAccount(tx, *candidate.AccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrTokenInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	if account.LastPasswordChange != nil && !candidate.CreatedAt.After(*account.LastPasswordChange) {
		return nil, nil, ErrTokenInvalid
	}
	var method models.RecoveryEmail
	if err := tx.First(&method, "account_id = ?", account.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = ErrTokenInvalid
		}
		return nil, nil, err
	}
	external, err := RecoveryEmailExternal(tx, method.Address)
	if err != nil {
		return nil, nil, err
	}
	if !external {
		return nil, nil, ErrTokenInvalid
	}
	if purpose == models.OneTimeTokenRecoverEmail && method.VerifiedAt == nil {
		return nil, nil, ErrTokenInvalid
	}
	if _, err := redeem(tx, token, purpose); err != nil {
		return nil, nil, err
	}
	return account, &method, nil
}

// MintRecoveryEmail issues only to an active account's verified external
// method. Unknown, disabled and unverified accounts all return ErrTokenInvalid.
func MintRecoveryEmail(ctx context.Context, tx *gorm.DB, address string) (*models.Mailbox, string, string, time.Time, error) {
	tx = tx.WithContext(ctx)
	var candidate models.Mailbox
	if err := tx.Where("address = ?", address).First(&candidate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = ErrTokenInvalid
		}
		return nil, "", "", time.Time{}, err
	}
	account, err := lockRecoveryAccount(tx, candidate.ID)
	if err != nil {
		return nil, "", "", time.Time{}, err
	}
	var method models.RecoveryEmail
	if err := tx.Where("account_id = ? AND verified_at IS NOT NULL", account.ID).First(&method).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = ErrTokenInvalid
		}
		return nil, "", "", time.Time{}, err
	}
	external, err := RecoveryEmailExternal(tx, method.Address)
	if err != nil {
		return nil, "", "", time.Time{}, err
	}
	if !external {
		return nil, "", "", time.Time{}, ErrTokenInvalid
	}
	if err := tx.Model(&models.OneTimeToken{}).Where("account_id = ? AND purpose = ? AND used_at IS NULL", account.ID, models.OneTimeTokenRecoverEmail).Update("used_at", time.Now()).Error; err != nil {
		return nil, "", "", time.Time{}, err
	}
	token, expires, err := mint(tx, models.OneTimeTokenRecoverEmail, &account.ID, DefaultTokenLifetime)
	return account, method.Address, token, expires, err
}
