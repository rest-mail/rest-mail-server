package repositories

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// ErrBadAPIKeyPermission is a permission an API key cannot hold: no
// capability, the wildcard capability, or a scope that is none of "*",
// "domain:<name>" and "mailbox:<address>".
var ErrBadAPIKeyPermission = errors.New(`each permission needs a capability other than "*" and a scope of "*", "domain:<name>" or "mailbox:<address>"`)

// NormalizeAPIKeyPermissions checks permissions and returns them as an API key
// stores them: scopes in lower case, duplicates dropped, order kept. An empty
// list is valid, and lets the key do nothing.
func NormalizeAPIKeyPermissions(permissions []auth.ScopedPermission) ([]auth.ScopedPermission, error) {
	out := make([]auth.ScopedPermission, 0, len(permissions))
	seen := make(map[auth.ScopedPermission]bool, len(permissions))
	for _, p := range permissions {
		if p.Capability == "" || p.Capability == grants.Wildcard {
			return nil, ErrBadAPIKeyPermission
		}
		scope, err := grants.ParseScope(p.Scope)
		if err != nil {
			return nil, ErrBadAPIKeyPermission
		}
		p.Scope = scope
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// apiKeyScope is an API key's scope: its account, and its permissions.
func apiKeyScope(accountID uint, permissions []auth.ScopedPermission) models.CredentialScope {
	return models.CredentialScope{MailboxID: accountID, Capabilities: []string{}, Permissions: permissions}
}

// CreateAPIKey makes a new API key for an account, holding permissions, and
// returns it with the only copy of the key there will ever be. createdBy is the
// account making it. The permissions are checked (NormalizeAPIKeyPermissions);
// whether the account holds them is not, here: a key may only ever use what
// its account holds at the time of each request.
func (r *CredentialRepository) CreateAPIKey(accountID, createdBy uint, name string, permissions []auth.ScopedPermission, expiresAt *time.Time) (*models.Credential, string, error) {
	permissions, err := NormalizeAPIKeyPermissions(permissions)
	if err != nil {
		return nil, "", err
	}
	for attempt := 0; ; attempt++ {
		key, publicID, secretHash, err := auth.NewAPIKey()
		if err != nil {
			return nil, "", err
		}
		by := createdBy
		cred := &models.Credential{
			AccountID:   accountID,
			Kind:        models.CredentialKindAPIKey,
			Name:        name,
			PublicID:    publicID,
			SecretHash:  secretHash,
			Scope:       apiKeyScope(accountID, permissions),
			CreatedByID: &by,
			ExpiresAt:   expiresAt,
		}
		err = r.db.Create(cred).Error
		if err == nil {
			return cred, key, nil
		}
		if attempt < 2 && IsUniqueViolation(err) {
			continue
		}
		return nil, "", fmt.Errorf("failed to store the API key: %w", err)
	}
}

// VerifyAPIKey checks an API key. It finds the key by its public id and
// compares the secret's hash in constant time; an unknown public id is
// compared against a dummy hash, so it costs the same. Only once the secret
// matches does it say why a real key is refused: revoked, expired, or its
// account gone, inactive or still in setup. It reads the row on every call,
// so a key revoked or expired, or an account disabled, stops working at the
// next request.
//
// A refusal is a *auth.CredentialRefusal; any other error means the key could
// not be checked.
func (r *CredentialRepository) VerifyAPIKey(key string, now time.Time) (*models.Credential, *models.Mailbox, error) {
	publicID, secret, ok := auth.ParseAPIKey(key)
	if !ok {
		return nil, nil, &auth.CredentialRefusal{Reason: auth.CredentialMalformed}
	}
	var cred models.Credential
	err := r.db.Where("public_id = ? AND kind = ?", publicID, models.CredentialKindAPIKey).Take(&cred).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		auth.APIKeySecretMatches(secret, "")
		return nil, nil, &auth.CredentialRefusal{Reason: auth.CredentialUnknown, PublicID: publicID}
	}
	if err != nil {
		return nil, nil, err
	}
	if !auth.APIKeySecretMatches(secret, cred.SecretHash) {
		return nil, nil, &auth.CredentialRefusal{Reason: auth.CredentialWrongSecret, PublicID: publicID}
	}
	refused := func(reason string) error {
		id := cred.ID
		return &auth.CredentialRefusal{Reason: reason, PublicID: publicID, CredentialID: &id}
	}

	var mailbox models.Mailbox
	err = r.db.Take(&mailbox, cred.AccountID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, refused(auth.CredentialAccountInactive)
	}
	if err != nil {
		return nil, nil, err
	}
	switch {
	case cred.RevokedAt != nil:
		return nil, nil, refused(auth.CredentialRevoked)
	case !cred.Usable(now):
		return nil, nil, refused(auth.CredentialExpired)
	case !mailbox.Active:
		return nil, nil, refused(auth.CredentialAccountInactive)
	case mailbox.PasswordChangeRequired || mailbox.BootstrapExpired(now):
		return nil, nil, refused(auth.CredentialAccountInSetup)
	}
	return &cred, &mailbox, nil
}

// RevokeAPIKey revokes one of an account's API keys. The row stays, for the
// audit trail.
func (r *CredentialRepository) RevokeAPIKey(accountID, id uint, now time.Time) error {
	res := r.db.Model(&models.Credential{}).
		Where("id = ? AND account_id = ? AND kind = ? AND revoked_at IS NULL", id, accountID, models.CredentialKindAPIKey).
		Update("revoked_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// RevokeAPIKeys revokes every unrevoked API key of the account's, expired ones
// too, and returns them.
func (r *CredentialRepository) RevokeAPIKeys(accountID uint, now time.Time) ([]models.Credential, error) {
	return r.RevokeKinds(accountID, now, models.CredentialKindAPIKey)
}

// APIKeyClaims are the claims a request made with cred, a key VerifyAPIKey
// has just accepted for account, runs with: the account's, marked as made with
// an API key and carrying its permissions. It is never a session: no
// TwoFactor, no AMR, no sid.
func APIKeyClaims(cred *models.Credential, account *models.Mailbox) *auth.Claims {
	permissions := append([]auth.ScopedPermission{}, cred.Scope.Permissions...)
	return &auth.Claims{
		RegisteredClaims:  jwt.RegisteredClaims{Subject: fmt.Sprintf("account:%d", account.ID)},
		AccountID:         account.ID,
		Address:           account.Address,
		TokenType:         "access",
		CredentialID:      cred.ID,
		CredentialKind:    models.CredentialKindAPIKey,
		APIKeyPermissions: permissions,
	}
}

// APIKeyVerifier checks an API key sent as a bearer token and returns the
// claims its request runs with (middleware.APIKeyVerify). It records the
// key's use, at most once per LastUsedEvery.
func (r *CredentialRepository) APIKeyVerifier() func(key string) (*auth.Claims, error) {
	return func(key string) (*auth.Claims, error) {
		now := time.Now()
		cred, account, err := r.VerifyAPIKey(key, now)
		if err != nil {
			return nil, err
		}
		if cred.LastUsedAt == nil || now.Sub(*cred.LastUsedAt) >= LastUsedEvery {
			if err := r.TouchLastUsed(cred.ID, now); err != nil {
				slog.Warn("could not record a credential's use", "credential_id", cred.ID, "error", err)
			}
		}
		return APIKeyClaims(cred, account), nil
	}
}
