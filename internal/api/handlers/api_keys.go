package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

const (
	ActionAPIKeyCreate    = "api_keys.create"
	ActionAPIKeyRevoke    = "api_keys.revoke"
	ActionAPIKeyUpdate    = "api_keys.update"
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
	Name        string             `json:"name" minLength:"1" pattern:"\\S" message:"required" missing:"required"`
	Password    string             `json:"password" minLength:"1" message:"required" missing:"required"`
	Permissions []apiKeyPermission `json:"permissions" minItems:"1" maxItems:"64" message:"must contain between 1 and 64 scoped permissions" missing:"must contain between 1 and 64 scoped permissions"`
	ExpiresAt   *time.Time         `json:"expires_at,omitempty"`
}

// apiKeyPermission is one permission asked for: a named capability at a scope. The handler
// reads what those names may be. It is an alias so that the description holds it in the body
// that uses it: Vertrag 0.42.0 does not look through a body's nested reference, and its
// fuzz.pin would then not engage on that body (tests/contract/probes.py).
type apiKeyPermission = struct {
	Capability string `json:"capability" minLength:"1" message:"each permission needs a named capability and a valid scope" missing:"each permission needs a named capability and a valid scope"`
	Scope      string `json:"scope" minLength:"1" message:"each permission needs a named capability and a valid scope" missing:"each permission needs a named capability and a valid scope"`
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

// parseAPIKeyPermissions reads the permissions asked for as a key stores them, adding to
// problems what is wrong with them. The error says they could not be read.
func parseAPIKeyPermissions(requested []apiKeyPermission, problems map[string]string) ([]auth.ScopedPermission, error) {
	if len(requested) == 0 || len(requested) > maxAPIKeyPermissions {
		problems["permissions"] = fmt.Sprintf("must contain between 1 and %d scoped permissions", maxAPIKeyPermissions)
	}
	asked := make([]auth.ScopedPermission, len(requested))
	for i, permission := range requested {
		asked[i] = auth.ScopedPermission{Capability: permission.Capability, Scope: permission.Scope}
	}
	permissions, err := repositories.NormalizeAPIKeyPermissions(asked)
	if err != nil {
		problems["permissions"] = "each permission needs a named capability and a valid scope"
	}
	return permissions, err
}

var (
	errAPIKeyBeyondGrants      = errors.New("API key exceeds held permissions")
	errAPIKeyUnknownCapability = errors.New("unknown API key capability")
)

// checkAPIKeyCeiling is the one rule for what a key may be given, when it is made and when it
// is changed: nothing beyond what the person doing it and the account it belongs to both hold
// now, and only capabilities that exist.
func checkAPIKeyCeiling(tx *gorm.DB, actorID, accountID uint, permissions []auth.ScopedPermission, now time.Time) error {
	actor, err := grants.Load(tx, actorID, now)
	if err != nil {
		return err
	}
	owner, err := grants.Load(tx, accountID, now)
	if err != nil {
		return err
	}
	if !apiKeyWithinGrants(actor, owner, permissions) {
		return errAPIKeyBeyondGrants
	}
	for _, permission := range permissions {
		var count int64
		if err := tx.Model(&models.Capability{}).Where("name = ?", permission.Capability).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return errAPIKeyUnknownCapability
		}
	}
	return nil
}

// apiKeyCeilingRefusal is the refusal for an error of checkAPIKeyCeiling, or nil for any other.
func apiKeyCeilingRefusal(err error) error {
	switch {
	case errors.Is(err, errAPIKeyBeyondGrants):
		return endpoint.Refuse(http.StatusForbidden, "forbidden", "API key permissions must be within the account's and your current grants")
	case errors.Is(err, errAPIKeyUnknownCapability):
		return endpoint.Invalid(map[string]string{"permissions": "contains an unknown capability"})
	}
	return nil
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
	permissions, err := parseAPIKeyPermissions(req.Permissions, problems)
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
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := checkAPIKeyCeiling(tx, claims.AccountID, accountID, permissions, now); err != nil {
			return err
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
	if refusal := apiKeyCeilingRefusal(err); refusal != nil {
		return createdAPIKey{}, refusal
	}
	if err != nil {
		return createdAPIKey{}, fmt.Errorf("create the API key: %w", err)
	}
	return createdAPIKey{toAPIKeyResponse(credential), secret}, nil
}

// updateAPIKeyBody is a key's new permissions, which replace the ones it holds. The current
// password is asked for as it is when making a key, because this can give a key more.
type updateAPIKeyBody struct {
	Password    string             `json:"password" minLength:"1" message:"required" missing:"required"`
	Permissions []apiKeyPermission `json:"permissions" minItems:"1" maxItems:"64" message:"must contain between 1 and 64 scoped permissions" missing:"must contain between 1 and 64 scoped permissions"`
}

type updateAPIKeyRequest struct {
	ID           uint32           `path:"id" minimum:"1" example:"1"`
	CredentialID uint32           `path:"credential_id" minimum:"1" example:"1"`
	Body         updateAPIKeyBody `body:""`
}

// updateAPIKey changes what a key may do and keeps its secret, so nothing that uses it has to
// change. It takes what making one takes. A request made with the key reads the stored
// permissions each time (VerifyAPIKey), so the very next one runs with the new ones.
func (h *CredentialHandler) updateAPIKey(r *http.Request, in updateAPIKeyRequest) (apiKeyResponse, error) {
	h = h.forRequest(r)
	claims, accountID, err := h.managedAccount(r, in.ID)
	if err != nil {
		return apiKeyResponse{}, err
	}
	if err := RequireFreshTwoFactor(r); err != nil {
		return apiKeyResponse{}, err
	}
	problems := map[string]string{}
	permissions, _ := parseAPIKeyPermissions(in.Body.Permissions, problems)
	if len(problems) != 0 {
		return apiKeyResponse{}, endpoint.Invalid(problems)
	}
	if err := h.checkCurrentPassword(claims.AccountID, in.Body.Password); err != nil {
		return apiKeyResponse{}, err
	}

	now := h.now()
	var credential *models.Credential
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := checkAPIKeyCeiling(tx, claims.AccountID, accountID, permissions, now); err != nil {
			return err
		}
		var previous []auth.ScopedPermission
		var err error
		credential, previous, err = repositories.NewCredentialRepository(tx).UpdateAPIKeyPermissions(accountID, uint(in.CredentialID), permissions)
		if err != nil {
			return err
		}
		before, err := json.Marshal(previous)
		if err != nil {
			return err
		}
		after, err := json.Marshal(credential.Scope.Permissions)
		if err != nil {
			return err
		}
		e := middleware.NewAuditEntry(ActionAPIKeyUpdate, r, now)
		e.Target["account_id"] = strconv.FormatUint(uint64(accountID), 10)
		e.Target["credential_id"] = strconv.FormatUint(uint64(credential.ID), 10)
		e.Target["public_id"], e.Target["name"] = credential.PublicID, credential.Name
		e.Target["previous_permissions"], e.Target["permissions"] = string(before), string(after)
		return audit.Append(tx, e)
	})
	if refusal := apiKeyCeilingRefusal(err); refusal != nil {
		return apiKeyResponse{}, refusal
	}
	if errors.Is(err, repositories.ErrCredentialNotFound) {
		return apiKeyResponse{}, endpoint.Refuse(http.StatusNotFound, "not_found", "API key not found")
	}
	if err != nil {
		return apiKeyResponse{}, fmt.Errorf("update the API key: %w", err)
	}
	return toAPIKeyResponse(credential), nil
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
