package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

const (
	ActionAPIKeyCreate    = "api_keys.create"
	ActionAPIKeyRevoke    = "api_keys.revoke"
	maxAPIKeyPermissions  = 64
	defaultAPIKeyLifetime = 90 * 24 * time.Hour
)

type apiKeyResponse struct {
	credentialResponse
	Permissions []auth.ScopedPermission `json:"permissions"`
}

func toAPIKeyResponse(c *models.Credential) apiKeyResponse {
	return apiKeyResponse{toCredentialResponse(c), append([]auth.ScopedPermission{}, c.Scope.Permissions...)}
}

// listAPIKeys lists metadata only. A key's secret is returned once, at creation.
func (h *CredentialHandler) listAPIKeys(r *http.Request, in accountPath) (endpoint.List[apiKeyResponse], error) {
	h = h.forRequest(r)
	_, accountID, err := h.managedAccount(r, in.ID)
	if err != nil {
		return endpoint.List[apiKeyResponse]{}, err
	}
	keys, err := h.repo.ListForAccount(accountID, models.CredentialKindAPIKey)
	if err != nil {
		return endpoint.List[apiKeyResponse]{}, fmt.Errorf("list API keys: %w", err)
	}
	out := make([]apiKeyResponse, len(keys))
	for i := range keys {
		out[i] = toAPIKeyResponse(&keys[i])
	}
	return endpoint.List[apiKeyResponse]{Data: out}, nil
}

// createAPIKeyBody is what the mobile app sends. The handler checks every field, as before, so
// a refusal names each field at fault at once: a missing password is the 403
// reauth_required, and missing permissions are refused with the rule they break.
type createAPIKeyBody struct {
	Name        string             `json:"name"`
	Password    string             `json:"password,omitempty"`
	Permissions []apiKeyPermission `json:"permissions,omitempty"`
	ExpiresAt   *time.Time         `json:"expires_at,omitempty"`
}

// apiKeyPermission is one permission asked for. A blank capability or scope is refused by the
// handler with the permissions rule, as before.
type apiKeyPermission struct {
	Capability string `json:"capability,omitempty"`
	Scope      string `json:"scope,omitempty"`
}

type createAPIKeyRequest struct {
	ID   uint32           `path:"id" minimum:"1" example:"1"`
	Body createAPIKeyBody `body:""`
}

// createdAPIKey is the answer to creating one: the key, and its token, shown this once and
// never again.
type createdAPIKey struct {
	apiKeyResponse
	Token string `json:"token"`
}

// apiKeyWithinGrants checks the creator and the account the key belongs to.
// Every use also checks the owner's live grants, including their expiry.
func apiKeyWithinGrants(creator, owner grants.Held, permissions []auth.ScopedPermission) bool {
	for _, permission := range permissions {
		if !creator.Allows(permission.Capability, permission.Scope) || !owner.Allows(permission.Capability, permission.Scope) {
			return false
		}
	}
	return true
}

// createAPIKey needs a password-authenticated, two-factor session and the
// creator's current password. Machine credentials never satisfy this check.
func (h *CredentialHandler) createAPIKey(r *http.Request, in createAPIKeyRequest) (createdAPIKey, error) {
	return h.createScopedCredential(r, in, models.CredentialKindAPIKey)
}

func (h *CredentialHandler) createScopedCredential(r *http.Request, in createAPIKeyRequest, kind string) (createdAPIKey, error) {
	h = h.forRequest(r)
	claims, accountID, err := h.managedAccount(r, in.ID)
	if err != nil {
		return createdAPIKey{}, err
	}
	if err := RequireFreshTwoFactor(r); err != nil {
		return createdAPIKey{}, err
	}
	req := in.Body
	req.Name = strings.TrimSpace(req.Name)
	now := h.now()
	problems := map[string]string{}
	if req.Name == "" {
		problems["name"] = "required"
	} else if utf8.RuneCountInString(req.Name) > maxCredentialNameLength || strings.IndexFunc(req.Name, unicode.IsControl) >= 0 {
		problems["name"] = "must contain at most 100 characters and no control characters"
	}
	if len(req.Permissions) == 0 || len(req.Permissions) > maxAPIKeyPermissions {
		problems["permissions"] = fmt.Sprintf("must contain between 1 and %d scoped permissions", maxAPIKeyPermissions)
	}
	asked := make([]auth.ScopedPermission, len(req.Permissions))
	for i, permission := range req.Permissions {
		asked[i] = auth.ScopedPermission{Capability: permission.Capability, Scope: permission.Scope}
	}
	permissions, err := repositories.NormalizeAPIKeyPermissions(asked)
	if err != nil {
		problems["permissions"] = "each permission needs a named capability and a valid scope"
	}
	if kind == models.CredentialKindClientSecret && err == nil {
		if _, err := repositories.NormalizeClientSecretPermissions(permissions); err != nil {
			problems["permissions"] = "client secrets may hold mailbox capabilities and ordinary domain administration, never human-2FA-only capabilities"
		}
	}
	if req.ExpiresAt == nil {
		expires := now.Add(defaultAPIKeyLifetime)
		req.ExpiresAt = &expires
	} else if !req.ExpiresAt.After(now) {
		problems["expires_at"] = "must be in the future"
	}
	if len(problems) != 0 {
		return createdAPIKey{}, endpoint.Invalid(problems)
	}
	if err := h.checkCurrentPassword(claims.AccountID, req.Password); err != nil {
		return createdAPIKey{}, err
	}

	var credential *models.Credential
	var secret string
	errPermission := errors.New("API key exceeds held permissions")
	errUnknown := errors.New("unknown API key capability")
	err = h.db.Transaction(func(tx *gorm.DB) error {
		creator, err := grants.Load(tx, claims.AccountID, now)
		if err != nil {
			return err
		}
		owner, err := grants.Load(tx, accountID, now)
		if err != nil {
			return err
		}
		if !apiKeyWithinGrants(creator, owner, permissions) {
			return errPermission
		}
		for _, permission := range permissions {
			var count int64
			if err := tx.Model(&models.Capability{}).Where("name = ?", permission.Capability).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return errUnknown
			}
		}
		repo := repositories.NewCredentialRepository(tx)
		action := ActionAPIKeyCreate
		if kind == models.CredentialKindClientSecret {
			credential, secret, err = repo.CreateClientSecret(accountID, claims.AccountID, req.Name, permissions, req.ExpiresAt)
			action = ActionClientSecretCreate
		} else {
			credential, secret, err = repo.CreateAPIKey(accountID, claims.AccountID, req.Name, permissions, req.ExpiresAt)
		}
		if err != nil {
			return err
		}
		e := middleware.NewAuditEntry(action, r, now)
		e.Status = http.StatusCreated
		e.Target["account_id"] = strconv.FormatUint(uint64(accountID), 10)
		e.Target["credential_id"] = strconv.FormatUint(uint64(credential.ID), 10)
		e.Target["public_id"], e.Target["name"] = credential.PublicID, credential.Name
		return audit.Append(tx, e)
	})
	switch {
	case errors.Is(err, errPermission):
		return createdAPIKey{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "API key permissions must be within the account's and your current grants")
	case errors.Is(err, errUnknown):
		return createdAPIKey{}, endpoint.Invalid(map[string]string{"permissions": "contains an unknown capability"})
	case err != nil:
		return createdAPIKey{}, fmt.Errorf("create the API key: %w", err)
	}
	return createdAPIKey{toAPIKeyResponse(credential), secret}, nil
}

// revokeAPIKey preserves the credential and its audit history. It stops being
// accepted on the next request because the credential is checked every time.
func (h *CredentialHandler) revokeAPIKey(r *http.Request, in credentialPath) (endpoint.Empty, error) {
	h = h.forRequest(r)
	_, accountID, err := h.managedAccount(r, in.ID)
	if err != nil {
		return endpoint.Empty{}, err
	}
	if err := RequireTwoFactor(r); err != nil {
		return endpoint.Empty{}, err
	}
	id := in.CredentialID
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := repositories.NewCredentialRepository(tx).RevokeAPIKey(accountID, uint(id), h.now()); err != nil {
			return err
		}
		e := middleware.NewAuditEntry(ActionAPIKeyRevoke, r, h.now())
		e.Status = http.StatusNoContent
		e.Target["account_id"] = strconv.FormatUint(uint64(accountID), 10)
		e.Target["credential_id"] = strconv.FormatUint(uint64(id), 10)
		return audit.Append(tx, e)
	})
	if errors.Is(err, repositories.ErrCredentialNotFound) {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "API key not found")
	}
	if err != nil {
		return endpoint.Empty{}, fmt.Errorf("revoke the API key: %w", err)
	}
	return endpoint.Empty{}, nil
}
