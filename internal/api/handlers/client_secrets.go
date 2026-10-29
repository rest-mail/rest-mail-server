package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

const ActionClientSecretCreate = "client_secrets.create"
const ActionClientSecretRevoke = "client_secrets.revoke"

type createdClientSecret struct {
	apiKeyResponse
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func (h *CredentialHandler) registerClientSecrets(rs endpoint.Routes, checks SessionRouteChecks) {
	tags := []string{"Credentials"}
	security := handlerChecked(models.CapMailboxManage, "CookieAuth", "BearerAuth")
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/client-secrets", OperationID: "listClientSecrets", Summary: "List an account's client secrets", Tags: tags, Errors: []int{403, 404}, Security: security, Middleware: []endpoint.Middleware{noStore}}, h.listClientSecrets)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/client-secrets", OperationID: "createClientSecret", Summary: "Make a scoped OAuth client secret", Tags: tags, Status: 201, Errors: []int{403, 404}, Security: security,
		Description: "Requires a human session signed in with a second factor, the current password, and mailbox:manage on the account. Returns client_id and client_secret once; stores only the secret hash. Permissions must be within both the creator's and the account's live grants. The client-secret tier accepts mail:read, mail:write, mail:send and mail:receive; domains:read, mailboxes:read and pipelines:read/write/delete require a domain scope. It rejects all other capabilities, including account/credential creation, granting, password/2FA, audit, retention and server-ceiling controls. expires_at defaults to 90 days. Credential creation is audited as client_secrets.create with actor and credential id. Client secrets exchange at /api/v1/oauth/token; they are never accepted as bearer API keys or mail-client passwords.",
		Middleware:  []endpoint.Middleware{checks.Throttle, checks.Audit(ActionClientSecretCreate, middleware.AuditFailuresOnly()), noStore}}, h.createClientSecret)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/accounts/{id}/client-secrets/{credential_id}", OperationID: "revokeClientSecret", Summary: "Revoke a client secret and its tokens", Tags: tags, Status: 204, Errors: []int{403, 404}, Security: security, Middleware: []endpoint.Middleware{checks.Audit(ActionClientSecretRevoke, middleware.AuditFailuresOnly())}}, h.revokeClientSecret)
}

func (h *CredentialHandler) createClientSecret(r *http.Request, in createAPIKeyRequest) (createdClientSecret, error) {
	claims := middleware.GetClaims(r)
	if claims == nil || claims.CredentialID != 0 || !claims.TwoFactor {
		return createdClientSecret{}, endpoint.Refuse(http.StatusForbidden, "two_factor_required", "Sign in with a second factor to create client secrets")
	}
	result, err := h.createScopedCredential(r, in, models.CredentialKindClientSecret)
	if err != nil {
		return createdClientSecret{}, err
	}
	return createdClientSecret{result.apiKeyResponse, result.PublicID, result.Token}, nil
}

func (h *CredentialHandler) listClientSecrets(r *http.Request, in accountPath) (endpoint.List[apiKeyResponse], error) {
	h = h.forRequest(r)
	_, id, err := h.managedAccount(r, in.ID)
	if err != nil {
		return endpoint.List[apiKeyResponse]{}, err
	}
	credentials, err := h.repo.ListForAccount(id, models.CredentialKindClientSecret)
	if err != nil {
		return endpoint.List[apiKeyResponse]{}, fmt.Errorf("list client secrets: %w", err)
	}
	out := make([]apiKeyResponse, len(credentials))
	for i := range credentials {
		out[i] = toAPIKeyResponse(&credentials[i])
	}
	return endpoint.List[apiKeyResponse]{Data: out}, nil
}

func (h *CredentialHandler) revokeClientSecret(r *http.Request, in credentialPath) (endpoint.Empty, error) {
	h = h.forRequest(r)
	claims, id, err := h.managedAccount(r, in.ID)
	if err != nil {
		return endpoint.Empty{}, err
	}
	if !claims.TwoFactor {
		return endpoint.Empty{}, endpoint.Refuse(403, "two_factor_required", "Sign in with two-factor authentication to revoke client secrets")
	}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := repositories.NewCredentialRepository(tx).RevokeClientSecret(id, uint(in.CredentialID), h.now()); err != nil {
			return err
		}
		e := middleware.NewAuditEntry(ActionClientSecretRevoke, r, h.now())
		e.Status = 204
		e.Target["account_id"] = strconv.FormatUint(uint64(id), 10)
		e.Target["credential_id"] = strconv.FormatUint(uint64(in.CredentialID), 10)
		return audit.Append(tx, e)
	})
	if errors.Is(err, repositories.ErrCredentialNotFound) {
		return endpoint.Empty{}, endpoint.Refuse(404, "not_found", "Client secret not found")
	}
	if err != nil {
		return endpoint.Empty{}, fmt.Errorf("revoke client secret: %w", err)
	}
	return endpoint.Empty{}, nil
}
