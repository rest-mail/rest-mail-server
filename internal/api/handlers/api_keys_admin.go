package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// adminAPIKey is one API key as an administrator sees it: its metadata and whose
// it is. A key's secret is returned once, at creation, and never here.
type adminAPIKey struct {
	apiKeyResponse
	AccountID      uint   `json:"account_id"`
	AccountAddress string `json:"account_address"`
}

type listAdminAPIKeysRequest struct {
	// Owner is the address of the account the key proves.
	Owner *string `query:"owner" format:"idn-email"`
	// Capability keeps keys holding this permission, at any scope.
	Capability *string `query:"capability"`
	// LastUsedBefore keeps keys last used before this time, and never-used ones.
	LastUsedBefore *time.Time `query:"last_used_before" message:"must be an RFC 3339 time"`
	// ExpiresBefore keeps keys that expire before this time.
	ExpiresBefore *time.Time `query:"expires_before" message:"must be an RFC 3339 time"`
	Limit         *int       `query:"limit" minimum:"1" maximum:"200" message:"must be between 1 and 200"`
	Offset        *int       `query:"offset" minimum:"0" message:"must not be negative"`
}

// RegisterAdmin mounts the view of API keys across accounts. onList limits it to
// the accounts the administrator's accounts:read covers (#1387).
func (h *CredentialHandler) RegisterAdmin(rs endpoint.Routes, onList func(string) endpoint.Middleware) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/api-keys",
		OperationID: "listAdminAPIKeys", Summary: "List API keys across accounts", Tags: []string{"Admin: Mailboxes"}, Status: http.StatusOK,
		Description: "Unrevoked keys of the accounts the caller may read, newest first. Secrets are never returned. Revoke one through its account's own route.",
		Middleware:  []endpoint.Middleware{onList(middleware.CapAccountsRead)}}, h.listAdmin)
}

func (h *CredentialHandler) listAdmin(r *http.Request, in listAdminAPIKeysRequest) (endpoint.List[adminAPIKey], error) {
	h = h.forRequest(r)
	limit, offset := pagination(in.Limit, in.Offset, defaultListLimit, maxListLimit)

	query := h.db.Model(&models.Credential{}).
		Joins("JOIN mailboxes ON mailboxes.id = credentials.account_id").
		Where("credentials.kind = ? AND credentials.revoked_at IS NULL", models.CredentialKindAPIKey)
	query = filterAdminList(query, r, middleware.CapAccountsRead, "mailboxes")
	if in.Owner != nil {
		query = query.Where("lower(mailboxes.address) = ?", strings.ToLower(*in.Owner))
	}
	if in.Capability != nil {
		query = query.Where("EXISTS (SELECT 1 FROM jsonb_array_elements(credentials.scope->'permissions') AS p WHERE p->>'capability' = ?)", *in.Capability)
	}
	if in.LastUsedBefore != nil {
		query = query.Where("credentials.last_used_at IS NULL OR credentials.last_used_at < ?", *in.LastUsedBefore)
	}
	if in.ExpiresBefore != nil {
		query = query.Where("credentials.expires_at < ?", *in.ExpiresBefore)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return endpoint.List[adminAPIKey]{}, fmt.Errorf("count API keys: %w", err)
	}
	var rows []struct {
		models.Credential
		AccountAddress string
	}
	if err := query.Select("credentials.*, mailboxes.address AS account_address").
		Order("credentials.created_at DESC, credentials.id DESC").Limit(limit).Offset(offset).Scan(&rows).Error; err != nil {
		return endpoint.List[adminAPIKey]{}, fmt.Errorf("list API keys: %w", err)
	}
	out := make([]adminAPIKey, len(rows))
	for i := range rows {
		out[i] = adminAPIKey{toAPIKeyResponse(&rows[i].Credential), rows[i].AccountID, rows[i].AccountAddress}
	}
	return endpoint.List[adminAPIKey]{Data: out, Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total}}, nil
}
