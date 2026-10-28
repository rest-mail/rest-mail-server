package handlers

import (
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
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
	var domain models.Domain
	if err := h.db.WithContext(r.Context()).Select("name").Take(&domain, id).Error; err != nil {
		return endpoint.Refuse(403, "forbidden", "Insufficient permissions on the pipeline's domain")
	}
	if !scopedDomainAllowed(r, capability, domain.Name) {
		return endpoint.Refuse(403, "forbidden", "Insufficient permissions on the pipeline's domain")
	}
	return nil
}
