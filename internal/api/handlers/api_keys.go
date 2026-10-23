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

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
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

// ListAPIKeys lists metadata only. A key's secret is returned once, at creation.
func (h *CredentialHandler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	_, accountID, ok := h.managedAccount(w, r)
	if !ok {
		return
	}
	keys, err := h.repo.ListForAccount(accountID, models.CredentialKindAPIKey)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list API keys")
		return
	}
	out := make([]apiKeyResponse, len(keys))
	for i := range keys {
		out[i] = toAPIKeyResponse(&keys[i])
	}
	w.Header().Set("Cache-Control", "no-store")
	respond.List(w, out, nil)
}

type createAPIKeyRequest struct {
	Name        string                  `json:"name"`
	Password    string                  `json:"password"`
	Permissions []auth.ScopedPermission `json:"permissions"`
	ExpiresAt   *time.Time              `json:"expires_at"`
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

// CreateAPIKey needs a password-authenticated, two-factor session and the
// creator's current password. Machine credentials never satisfy this check.
func (h *CredentialHandler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, accountID, ok := h.managedAccount(w, r)
	if !ok {
		return
	}
	if !claims.TwoFactor {
		respond.Error(w, http.StatusForbidden, "two_factor_required", "Sign in with two-factor authentication to create API keys")
		return
	}
	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
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
	permissions, err := repositories.NormalizeAPIKeyPermissions(req.Permissions)
	if err != nil {
		problems["permissions"] = "each permission needs a named capability and a valid scope"
	}
	if req.ExpiresAt == nil {
		expires := now.Add(defaultAPIKeyLifetime)
		req.ExpiresAt = &expires
	} else if !req.ExpiresAt.After(now) {
		problems["expires_at"] = "must be in the future"
	}
	if len(problems) != 0 {
		respond.ValidationError(w, problems)
		return
	}
	if !h.checkCurrentPassword(w, claims.AccountID, req.Password) {
		return
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
		credential, secret, err = repositories.NewCredentialRepository(tx).CreateAPIKey(accountID, claims.AccountID, req.Name, permissions, req.ExpiresAt)
		if err != nil {
			return err
		}
		e := middleware.NewAuditEntry(ActionAPIKeyCreate, r, now)
		e.Status = http.StatusCreated
		e.Target["account_id"] = strconv.FormatUint(uint64(accountID), 10)
		e.Target["credential_id"] = strconv.FormatUint(uint64(credential.ID), 10)
		e.Target["public_id"], e.Target["name"] = credential.PublicID, credential.Name
		return audit.Append(tx, e)
	})
	switch {
	case errors.Is(err, errPermission):
		respond.Error(w, http.StatusForbidden, "forbidden", "API key permissions must be within the account's and your current grants")
		return
	case errors.Is(err, errUnknown):
		respond.ValidationError(w, map[string]string{"permissions": "contains an unknown capability"})
		return
	case err != nil:
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create the API key")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond.Data(w, http.StatusCreated, struct {
		apiKeyResponse
		Token string `json:"token"`
	}{toAPIKeyResponse(credential), secret})
}

// RevokeAPIKey preserves the credential and its audit history. It stops being
// accepted on the next request because the credential is checked every time.
func (h *CredentialHandler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	claims, accountID, ok := h.managedAccount(w, r)
	if !ok {
		return
	}
	if !claims.TwoFactor {
		respond.Error(w, http.StatusForbidden, "two_factor_required", "Sign in with two-factor authentication to revoke API keys")
		return
	}
	id, err := pathID(r, "credential_id")
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid API key ID")
		return
	}
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
		respond.Error(w, http.StatusNotFound, "not_found", "API key not found")
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to revoke the API key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
