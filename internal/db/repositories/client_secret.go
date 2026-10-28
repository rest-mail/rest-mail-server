package repositories

import (
	"errors"
	"fmt"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

var ErrClientSecretTier = errors.New("client-secret permissions exceed its credential tier")

func NormalizeClientSecretPermissions(asked []auth.ScopedPermission) ([]auth.ScopedPermission, error) {
	permissions, err := NormalizeAPIKeyPermissions(asked)
	if err != nil {
		return nil, err
	}
	for _, p := range permissions {
		if !auth.ClientSecretPermissionAllowed(p) {
			return nil, ErrClientSecretTier
		}
	}
	return permissions, nil
}

func (r *CredentialRepository) CreateClientSecret(accountID, createdBy uint, name string, permissions []auth.ScopedPermission, expiresAt *time.Time) (*models.Credential, string, error) {
	permissions, err := NormalizeClientSecretPermissions(permissions)
	if err != nil {
		return nil, "", err
	}
	for attempt := 0; ; attempt++ {
		id, secret, hash, err := auth.NewClientSecret()
		if err != nil {
			return nil, "", err
		}
		by := createdBy
		cred := &models.Credential{AccountID: accountID, Kind: models.CredentialKindClientSecret, Name: name, PublicID: id, SecretHash: hash, Scope: apiKeyScope(accountID, permissions), CreatedByID: &by, ExpiresAt: expiresAt}
		err = r.db.Create(cred).Error
		if err == nil {
			return cred, secret, nil
		}
		if attempt < 2 && IsUniqueViolation(err) {
			continue
		}
		return nil, "", fmt.Errorf("store client secret: %w", err)
	}
}

func (r *CredentialRepository) clientSecretAccount(cred *models.Credential, now time.Time) (*models.Mailbox, error) {
	if cred.Kind != models.CredentialKindClientSecret || !cred.Usable(now) {
		return nil, auth.ErrInvalidCredentials
	}
	var account models.Mailbox
	if err := r.db.Take(&account, cred.AccountID).Error; err != nil {
		return nil, err
	}
	if !account.Active || account.PasswordChangeRequired || account.BootstrapExpired(now) {
		return nil, auth.ErrInvalidCredentials
	}
	if account.TwoFactorRequired {
		enrolled, err := r.HasTwoFactor(account.ID)
		if err != nil {
			return nil, err
		}
		if !enrolled {
			return nil, auth.ErrInvalidCredentials
		}
	}
	return &account, nil
}

// VerifyClientSecret never accepts another credential kind. Missing ids incur
// the same constant-time hash comparison as a wrong secret.
func (r *CredentialRepository) VerifyClientSecret(id, secret string, now time.Time) (*models.Credential, *models.Mailbox, error) {
	var cred models.Credential
	err := r.db.Where("public_id = ? AND kind = ?", id, models.CredentialKindClientSecret).Take(&cred).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		auth.AppPasswordSecretMatches(secret, "")
		return nil, nil, auth.ErrInvalidCredentials
	}
	if err != nil {
		return nil, nil, err
	}
	if !auth.AppPasswordSecretMatches(secret, cred.SecretHash) {
		return nil, nil, auth.ErrInvalidCredentials
	}
	account, err := r.clientSecretAccount(&cred, now)
	return &cred, account, err
}

// MachineTokenVerifier reloads the credential and live RBAC for every request.
// Only the intersection is exposed to authorization, even when a route's
// account-grant cache still contains a recently removed grant.
func (r *CredentialRepository) MachineTokenVerifier() func(*auth.Claims) error {
	return func(c *auth.Claims) error {
		var cred models.Credential
		if err := r.db.Take(&cred, c.MachineCredentialID).Error; err != nil {
			return err
		}
		if cred.AccountID != c.AccountID {
			return auth.ErrInvalidCredentials
		}
		account, err := r.clientSecretAccount(&cred, time.Now())
		if err != nil {
			return err
		}
		held, err := grants.Load(r.db, account.ID, time.Now())
		if err != nil {
			return err
		}
		c.CredentialID, c.CredentialKind = cred.ID, cred.Kind
		c.Address = account.Address
		c.TwoFactor, c.AMR = false, nil
		c.APIKeyPermissions = []auth.ScopedPermission{}
		for _, p := range cred.Scope.Permissions {
			if !auth.ClientSecretPermissionAllowed(p) {
				continue
			}
			for _, scope := range grants.IntersectScopes(held.Scopes(p.Capability), []string{p.Scope}) {
				c.APIKeyPermissions = append(c.APIKeyPermissions, auth.ScopedPermission{Capability: p.Capability, Scope: scope})
			}
		}
		return nil
	}
}

func (r *CredentialRepository) RevokeClientSecret(accountID, id uint, now time.Time) error {
	result := r.db.Model(&models.Credential{}).Where("id = ? AND account_id = ? AND kind = ? AND revoked_at IS NULL", id, accountID, models.CredentialKindClientSecret).Update("revoked_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}
