package handlers

import (
	"errors"
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// checkPipelineDomain bounds body-selected domains. URL-selected resources are
// checked by DomainResourceScopeOf before their handlers run.
func (h *PipelineHandler) checkPipelineDomain(r *http.Request, capability string, id uint) error {
	scopes, restricted := middleware.CapabilityScopes(r, capability)
	if !restricted {
		return nil
	}
	for _, scope := range scopes {
		if scope == "*" {
			return nil
		}
	}
	// A domain that does not exist is refused like one out of scope, so a
	// scoped caller cannot probe for other domains' IDs. A lookup that fails is
	// not a refusal: the caller may hold the rights, and must be told to retry.
	var domain models.Domain
	if err := h.db.WithContext(r.Context()).Select("name").Take(&domain, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Refuse(http.StatusForbidden, "forbidden", "Insufficient permissions")
		}
		return endpoint.Refuse(http.StatusServiceUnavailable, "unavailable", "Could not verify permissions")
	}
	if !scopedDomainAllowed(r, capability, domain.Name) {
		return endpoint.Refuse(http.StatusForbidden, "forbidden", "Insufficient permissions")
	}
	return nil
}
