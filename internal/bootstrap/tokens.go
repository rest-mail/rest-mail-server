package bootstrap

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

// Single-use tokens reach only the server's owner — the setup code a fresh
// install prints to its own output, `restmail-api mint-token` run on the
// server, or a request signed with an owner key — and are redeemed once over
// the public API. Possession of one therefore proves the redeemer was handed it
// by whoever controls the deployment, which is what "owning this server" means.

// DefaultTokenLifetime is how long a minted token can be redeemed. Long enough
// to scan a QR code or for an agent to finish a plan; short enough that a token
// left in a terminal's scrollback is soon worthless.
const DefaultTokenLifetime = 15 * time.Minute

// MinChosenPasswordLength is the shortest password a person may choose for an
// admin account. Long enough that a passphrase clears it and a short dictionary
// word does not.
const MinChosenPasswordLength = 12

var (
	// ErrAlreadyClaimed: a setup token was asked for, but the server has an
	// admin. Getting back in is reset-admin's job.
	ErrAlreadyClaimed = errors.New("the server already has an admin; use reset-admin to recover one")
	// ErrNoSuchAdmin: reset-admin named an account that does not exist.
	ErrNoSuchAdmin = errors.New("no such admin")
	// ErrTokenInvalid covers unknown, used, expired and wrong-purpose tokens
	// alike, so the answer reveals nothing about which.
	ErrTokenInvalid = errors.New("the token is not valid: unknown, already used, or expired")
	// ErrPasswordTooShort: a chosen password under MinChosenPasswordLength.
	ErrPasswordTooShort = fmt.Errorf("the password must be at least %d characters", MinChosenPasswordLength)
	// ErrUsernameRequired: claiming needs a name for the first admin.
	ErrUsernameRequired = errors.New("a username is required")
)

// MintSetupToken mints a token that claims this server, while it has no admin.
func MintSetupToken(database *gorm.DB, lifetime time.Duration) (string, time.Time, error) {
	var count int64
	if err := database.Model(&models.AdminUser{}).Count(&count).Error; err != nil {
		return "", time.Time{}, err
	}
	if count > 0 {
		return "", time.Time{}, ErrAlreadyClaimed
	}
	return mint(database, models.OneTimeTokenSetup, nil, lifetime)
}

// MintResetToken mints a token that lets the named admin choose a new password
// and enroll a new authenticator: the way back in when every other is lost.
func MintResetToken(database *gorm.DB, username string, lifetime time.Duration) (string, time.Time, error) {
	var admin models.AdminUser
	if err := database.Where("username = ?", username).First(&admin).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", time.Time{}, ErrNoSuchAdmin
		}
		return "", time.Time{}, err
	}
	return mint(database, models.OneTimeTokenResetAdmin, &admin.ID, lifetime)
}

func mint(database *gorm.DB, purpose string, adminID *uint, lifetime time.Duration) (string, time.Time, error) {
	if lifetime <= 0 {
		lifetime = DefaultTokenLifetime
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(lifetime)
	row := models.OneTimeToken{
		Purpose:     purpose,
		TokenHash:   hashToken(token),
		AdminUserID: adminID,
		ExpiresAt:   expires,
	}
	if err := database.Create(&row).Error; err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// redeem spends a token of the given purpose inside tx. One conditional UPDATE
// does the spending, so of two requests racing with the same token exactly one
// wins; if the surrounding transaction later fails, the token is unspent again.
func redeem(tx *gorm.DB, token, purpose string) (*models.OneTimeToken, error) {
	hash := hashToken(token)
	now := time.Now()
	res := tx.Model(&models.OneTimeToken{}).
		Where("token_hash = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?", hash, purpose, now).
		Update("used_at", now)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, ErrTokenInvalid
	}
	var row models.OneTimeToken
	if err := tx.Where("token_hash = ?", hash).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ClaimServer spends a setup token and makes its redeemer the first admin, with
// the username and password they chose. The account still owes an
// authenticator before it can use the admin API: TwoFactorRequired is set, and
// the session the caller is given next is a setup session.
func ClaimServer(database *gorm.DB, token, username, password string, twoFactorAvailable bool) (*models.AdminUser, error) {
	if username == "" {
		return nil, ErrUsernameRequired
	}
	if len(password) < MinChosenPasswordLength {
		return nil, ErrPasswordTooShort
	}
	if !twoFactorAvailable {
		return nil, errors.New("the first admin must enroll in 2FA, but TOTP_2FA_ENABLED is off")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	var admin models.AdminUser
	err = database.Transaction(func(tx *gorm.DB) error {
		// A minted token, or else the code the install printed, as typed.
		_, err := redeem(tx, token, models.OneTimeTokenSetup)
		if errors.Is(err, ErrTokenInvalid) {
			_, err = redeem(tx, NormalizeSetupCode(token), models.OneTimeTokenSetupCode)
		}
		if err != nil {
			return err
		}
		// Minted while there was no admin; one may have appeared since (a
		// bootstrap password, a second token). The server is claimed once.
		var count int64
		if err := tx.Model(&models.AdminUser{}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrAlreadyClaimed
		}
		var superadmin models.Role
		if err := tx.Where("name = ?", "superadmin").First(&superadmin).Error; err != nil {
			return fmt.Errorf("superadmin role missing (run EnsureRBAC first): %w", err)
		}
		now := time.Now()
		admin = models.AdminUser{
			Username:           username,
			PasswordHash:       hash,
			Active:             true,
			TwoFactorRequired:  true,
			LastPasswordChange: &now,
		}
		if err := tx.Create(&admin).Error; err != nil {
			return err
		}
		// Claimed: every other way to claim it is dead, the printed code included.
		if err := tx.Where("purpose IN ? AND used_at IS NULL",
			[]string{models.OneTimeTokenSetup, models.OneTimeTokenSetupCode}).
			Delete(&models.OneTimeToken{}).Error; err != nil {
			return err
		}
		return tx.Create(&models.UserRole{UserID: admin.ID, RoleID: superadmin.ID}).Error
	})
	if err != nil {
		return nil, err
	}
	return &admin, nil
}

// ResetAdmin spends a reset_admin token: the admin it names gets the password
// the redeemer chose, loses its authenticator (the case this exists for is
// having lost it) and must enroll a new one, and every session it had ends.
func ResetAdmin(database *gorm.DB, token, password string) (*models.AdminUser, error) {
	if len(password) < MinChosenPasswordLength {
		return nil, ErrPasswordTooShort
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	var admin models.AdminUser
	err = database.Transaction(func(tx *gorm.DB) error {
		row, err := redeem(tx, token, models.OneTimeTokenResetAdmin)
		if err != nil {
			return err
		}
		if row.AdminUserID == nil {
			return ErrTokenInvalid
		}
		if err := tx.First(&admin, *row.AdminUserID).Error; err != nil {
			return ErrNoSuchAdmin
		}
		now := time.Now()
		if err := tx.Model(&admin).Updates(map[string]any{
			"password_hash":            hash,
			"password_change_required": false,
			"two_factor_required":      true,
			"active":                   true,
			"last_password_change":     now,
			// Every token the account was issued is dead from now.
			"credential_epoch": gorm.Expr("credential_epoch + 1"),
		}).Error; err != nil {
			return err
		}
		// Read back what the database now holds — the bumped epoch above all —
		// or the session minted next would carry the old one and be refused.
		if err := tx.First(&admin, admin.ID).Error; err != nil {
			return err
		}
		if err := repositories.NewTwoFactorRepository(tx).Delete(models.TwoFactorUserTypeAdmin, admin.ID); err != nil {
			return err
		}
		return tx.Model(&models.RefreshToken{}).
			Where("user_type = ? AND subject_id = ? AND status = ?", "admin", admin.ID, models.RefreshTokenActive).
			Updates(map[string]any{"status": models.RefreshTokenRevoked, "updated_at": now}).Error
	})
	if err != nil {
		return nil, err
	}
	return &admin, nil
}
