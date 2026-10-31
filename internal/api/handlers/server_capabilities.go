package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// Where the server capability ceiling in force comes from.
const (
	serverCapabilitiesFromDatabase    = "database"
	serverCapabilitiesFromEnvironment = "environment"
)

// serverCapabilitiesResponse is the installation-wide capability ceiling: an
// account other than superadmin holds only what both its grants and this set
// allow.
type serverCapabilitiesResponse struct {
	Capabilities []string `json:"capabilities" example:"mail:read,mail:send"`
	// Source is "environment" while RESTMAIL_SERVER_CAPABILITIES sets the
	// ceiling; it cannot be changed through the API then.
	Source string `json:"source" enums:"database,environment"`
}

type updateServerCapabilitiesRequest struct {
	Capabilities *[]string `json:"capabilities,omitempty"`
}

// serverCapabilities reads the server capability ceiling in force; superadmin only.
// GET /api/v1/admin/server-capabilities
func (h *GrantHandler) serverCapabilities(r *http.Request, _ struct{}) (serverCapabilitiesResponse, error) {
	h = h.forRequest(r)
	claims := middleware.GetClaims(r)
	if err := grants.RequireSuperadmin(h.db, claims.AccountID); err != nil {
		return serverCapabilitiesResponse{}, serverCapabilitiesReadError(err)
	}
	names, overridden, err := grants.ServerCeiling(h.db, time.Now())
	if err != nil {
		return serverCapabilitiesResponse{}, serverCapabilitiesReadError(err)
	}
	return toServerCapabilitiesResponse(names, overridden), nil
}

// updateServerCapabilities replaces the stored server capability ceiling;
// superadmin only, refused while the environment sets it. The change and its
// audit entry (server_capabilities.update) commit together.
// PUT /api/v1/admin/server-capabilities {"capabilities":["mail:read","mail:send"]}
func (h *GrantHandler) updateServerCapabilities(r *http.Request, req updateServerCapabilitiesRequest) (serverCapabilitiesResponse, error) {
	h = h.forRequest(r)
	if req.Capabilities == nil {
		return serverCapabilitiesResponse{}, endpoint.Invalid(map[string]string{"capabilities": "required: the capability names the installation permits, [] for none"})
	}
	claims := middleware.GetClaims(r)
	var names []string
	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := grants.Lock(tx); err != nil {
			return err
		}
		if err := grants.RequireSuperadmin(tx, claims.AccountID); err != nil {
			return err
		}
		var err error
		names, err = grants.SetServerCeiling(tx, *req.Capabilities, middleware.RequestEntry(r, grants.ActionServerCapabilitiesUpdate, http.StatusOK))
		return err
	})
	if err != nil {
		return serverCapabilitiesResponse{}, serverCapabilitiesError(err)
	}
	h.cache.Invalidate()
	return toServerCapabilitiesResponse(names, false), nil
}

func toServerCapabilitiesResponse(names []string, overridden bool) serverCapabilitiesResponse {
	source := serverCapabilitiesFromDatabase
	if overridden {
		source = serverCapabilitiesFromEnvironment
	}
	if names == nil {
		names = []string{}
	}
	return serverCapabilitiesResponse{Capabilities: names, Source: source}
}

// serverCapabilitiesReadError is serverCapabilitiesError for the read: it
// changes nothing, so a failure is the plain internal error, not a failed
// grant change.
func serverCapabilitiesReadError(err error) error {
	if errors.Is(err, grants.ErrSuperadminRequired) {
		return serverCapabilitiesError(err)
	}
	return err
}

func serverCapabilitiesError(err error) error {
	var invalid *grants.InvalidCapabilityError
	switch {
	case errors.As(err, &invalid):
		return endpoint.Invalid(map[string]string{"capabilities": invalid.Error()})
	case errors.Is(err, grants.ErrSuperadminRequired):
		return endpoint.Refuse(http.StatusForbidden, "superadmin_required", err.Error())
	case errors.Is(err, grants.ErrServerOverridden):
		return endpoint.Refuse(http.StatusConflict, "server_capabilities_from_environment", err.Error())
	default:
		return grantChangeError(err)
	}
}
