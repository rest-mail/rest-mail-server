package bootstrap

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/audit"
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

// ResetPassword gives an account a new password, chosen by whoever ran the
// reset, and puts it back in setup: the password must be replaced at the next
// sign-in and an authenticator enrolled afresh, because the case this exists
// for is someone who has lost their way in — which may be the authenticator as
// much as the password.
//
// Every session the account had ends, and so does every app password: whoever
// had the old password could have made one. The refresh tokens are revoked as
// well as the epoch bumped, because a refresh re-mints at the current epoch
// rather than checking the presented one.
//
// The bootstrap expiry is cleared, so the deployment's own password can never
// re-arm the account afterwards: a restart must not quietly replace the
// password someone has just been handed.
func ResetPassword(database *gorm.DB, address, password string) (*models.Mailbox, error) {
	if len(password) < MinAdminPasswordLength {
		return nil, ErrResetPasswordTooShort
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	var account models.Mailbox
	err = database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("address = ?", strings.ToLower(strings.TrimSpace(address))).First(&account).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNoSuchAdmin
			}
			return err
		}
		if err := tx.Model(&account).Updates(map[string]any{
			"password_change_required": true,
			"two_factor_required":      true,
			"bootstrap_expires_at":     nil,
			"active":                   true,
			"credential_epoch":         gorm.Expr("credential_epoch + 1"),
		}).Error; err != nil {
			return err
		}
		if err := resetCredentials(tx, account.ID, hash, time.Now()); err != nil {
			return err
		}
		return tx.First(&account, account.ID).Error
	})
	if err != nil {
		return nil, err
	}
	return &account, nil
}

// CredentialRevokedAccountReset is the reason recorded when resetting an
// account revokes its app passwords.
const CredentialRevokedAccountReset = "account_reset"

// resetCredentials is what resetting an account does to its credentials, in
// tx: hash becomes its one password, its authenticator and recovery codes are
// revoked, and so is every app password (each recorded in the audit log), and
// every session ends.
func resetCredentials(tx *gorm.DB, accountID uint, hash string, now time.Time) error {
	creds := repositories.NewCredentialRepository(tx)
	if _, err := creds.SetPassword(accountID, hash, nil, now); err != nil {
		return err
	}
	if _, err := creds.RevokeKinds(accountID, now, models.CredentialKindTOTP, models.CredentialKindRecoveryCode); err != nil {
		return err
	}
	revoked, err := creds.RevokeAppPasswords(accountID, now)
	if err != nil {
		return err
	}
	for _, c := range revoked {
		if err := audit.Append(tx, &models.AuditEntry{
			OccurredAt: now,
			ActorKind:  models.AuditActorSystem,
			ActorName:  "system",
			Action:     "credentials.revoke",
			Outcome:    models.AuditOutcomeSuccess,
			Target: models.AuditTarget{
				"account_id":    strconv.FormatUint(uint64(accountID), 10),
				"credential_id": strconv.FormatUint(uint64(c.ID), 10),
				"public_id":     c.PublicID,
				"name":          c.Name,
				"reason":        CredentialRevokedAccountReset,
			},
		}); err != nil {
			return err
		}
	}
	return repositories.NewRefreshTokenRepository(tx).RevokeAllForAccount(accountID)
}

// ErrResetPasswordTooShort: a reset password is generated, not remembered, so
// it is held to the bootstrap password's length rather than a chosen one's.
var ErrResetPasswordTooShort = errors.New("a reset password must be at least 16 characters")

// SessionsRevoked is what RevokeAllSessions ended.
type SessionsRevoked struct {
	RefreshTokens int64 `json:"refresh_tokens"`
	Accounts      int64 `json:"accounts"`
}

// RevokeAllSessions signs everybody out: every refresh token, every account's
// access tokens.
//
// Refresh tokens go first, so nothing can be minted behind the sweep; a
// refresh also refuses a token from before its account's epoch moved on, so
// none minted earlier gets back in either way. A running API sees the new
// epochs within its session cache's lifetime (handlers.SessionCacheTTL).
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
		res = tx.Model(&models.Mailbox{}).
			Where("1 = 1").
			Update("credential_epoch", gorm.Expr("credential_epoch + 1"))
		if res.Error != nil {
			return res.Error
		}
		out.Accounts = res.RowsAffected
		return nil
	})
	return out, err
}
