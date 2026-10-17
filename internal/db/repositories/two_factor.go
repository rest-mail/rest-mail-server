package repositories

import (
	"errors"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// ErrTwoFactorNotFound is returned when the account has no TOTP credential
// in the state asked for.
var ErrTwoFactorNotFound = errors.New("two-factor enrollment not found")

// TwoFactorRepository keeps an account's two-factor authentication: its TOTP
// credentials and recovery codes, in the credentials table.
//
// Enrolling is two-phase, so a mistyped or misprovisioned secret cannot lock
// anyone out: Enroll stores an unconfirmed TOTP credential (and fresh
// recovery codes), and only Confirm, after a first valid code, makes it count.
// An unconfirmed one never gates a sign-in.
type TwoFactorRepository struct {
	db *gorm.DB
}

func NewTwoFactorRepository(db *gorm.DB) *TwoFactorRepository {
	return &TwoFactorRepository{db: db}
}

// Get returns the account's newest unrevoked TOTP credential, confirmed or
// pending.
func (r *TwoFactorRepository) Get(accountID uint) (*models.Credential, error) {
	return r.find(r.db.Where("account_id = ? AND kind = ? AND revoked_at IS NULL", accountID, models.CredentialKindTOTP))
}

// GetActive returns the account's newest confirmed, unrevoked TOTP
// credential: the one sign-ins are checked against.
func (r *TwoFactorRepository) GetActive(accountID uint) (*models.Credential, error) {
	return r.find(r.db.Where("account_id = ? AND kind = ? AND revoked_at IS NULL AND confirmed_at IS NOT NULL", accountID, models.CredentialKindTOTP))
}

func (r *TwoFactorRepository) find(q *gorm.DB) (*models.Credential, error) {
	var cred models.Credential
	err := q.Order("id DESC").First(&cred).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTwoFactorNotFound
	}
	if err != nil {
		return nil, err
	}
	return &cred, nil
}

// Active lists every confirmed, unrevoked TOTP credential of the account's: a
// code from any of them is a second factor.
func (r *TwoFactorRepository) Active(accountID uint) ([]models.Credential, error) {
	var creds []models.Credential
	err := r.db.Where("account_id = ? AND kind = ? AND revoked_at IS NULL AND confirmed_at IS NOT NULL", accountID, models.CredentialKindTOTP).
		Order("id").Find(&creds).Error
	return creds, err
}

// Enroll starts a new enrollment: it revokes any TOTP credential and recovery
// codes the account still has, and stores an unconfirmed TOTP credential with
// the encrypted secret and the recovery codes' hashes, in one transaction.
func (r *TwoFactorRepository) Enroll(accountID uint, encryptedSecret string, recoveryHashes []string) (*models.Credential, error) {
	var totp *models.Credential
	err := r.db.Transaction(func(tx *gorm.DB) error {
		creds := NewCredentialRepository(tx)
		if _, err := creds.RevokeKinds(accountID, time.Now(), models.CredentialKindTOTP, models.CredentialKindRecoveryCode); err != nil {
			return err
		}
		by := accountID
		totp = &models.Credential{
			AccountID:       accountID,
			Kind:            models.CredentialKindTOTP,
			Name:            "Authenticator",
			EncryptedSecret: encryptedSecret,
			Scope:           accountScope(accountID),
			CreatedByID:     &by,
		}
		if err := creds.create(totp); err != nil {
			return err
		}
		for _, h := range recoveryHashes {
			if err := creds.create(&models.Credential{
				AccountID:   accountID,
				Kind:        models.CredentialKindRecoveryCode,
				Name:        "Recovery code",
				SecretHash:  h,
				Scope:       accountScope(accountID),
				CreatedByID: &by,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return totp, nil
}

// Confirm makes a pending TOTP credential count.
func (r *TwoFactorRepository) Confirm(id uint) error {
	res := r.db.Model(&models.Credential{}).
		Where("id = ? AND kind = ? AND confirmed_at IS NULL AND revoked_at IS NULL", id, models.CredentialKindTOTP).
		Update("confirmed_at", time.Now())
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrTwoFactorNotFound
	}
	return nil
}

// Delete turns two-factor authentication off: every TOTP credential and
// recovery code of the account's is revoked. The rows stay.
func (r *TwoFactorRepository) Delete(accountID uint) error {
	_, err := NewCredentialRepository(r.db).RevokeKinds(accountID, time.Now(), models.CredentialKindTOTP, models.CredentialKindRecoveryCode)
	return err
}

// ConsumeRecoveryCode spends one of the account's recovery codes: it reports
// whether plaintext matched an unspent one, and revokes it if so. The guarded
// update makes a code redeemable once, even by two requests at the same time.
func (r *TwoFactorRepository) ConsumeRecoveryCode(accountID uint, plaintext string) (bool, error) {
	if plaintext == "" {
		return false, nil
	}
	var codes []models.Credential
	if err := r.db.Where("account_id = ? AND kind = ? AND revoked_at IS NULL", accountID, models.CredentialKindRecoveryCode).
		Find(&codes).Error; err != nil {
		return false, err
	}
	for i := range codes {
		if !auth.CheckRecoveryCode(plaintext, codes[i].SecretHash) {
			continue
		}
		now := time.Now()
		res := r.db.Model(&models.Credential{}).
			Where("id = ? AND revoked_at IS NULL", codes[i].ID).
			Updates(map[string]any{"revoked_at": now, "last_used_at": now})
		if res.Error != nil {
			return false, res.Error
		}
		return res.RowsAffected == 1, nil
	}
	return false, nil
}
