package handlers

import (
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// SuppressionHandler lists and clears a domain's suppressed recipients: the
// addresses its mail is no longer sent to after they hard bounced (#141).
type SuppressionHandler struct {
	db *gorm.DB
}

// NewSuppressionHandler creates a new SuppressionHandler.
func NewSuppressionHandler(db *gorm.DB) *SuppressionHandler {
	return &SuppressionHandler{db: db}
}

// list returns a domain's suppressions that haven't expired, newest first. An
// expired one no longer suppresses, so it isn't listed.
// GET /api/v1/admin/domains/{id}/suppressions
func (h *SuppressionHandler) list(r *http.Request, in domainPath) (endpoint.List[Suppression], error) {
	db := h.db.WithContext(r.Context())
	domainID := in.ID

	var suppressions []models.Suppression
	if err := db.Where("domain_id = ? AND expires_at > ?", domainID, time.Now()).
		Order("updated_at DESC").
		Find(&suppressions).Error; err != nil {
		return endpoint.List[Suppression]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list suppressions")
	}

	data := make([]Suppression, len(suppressions))
	for i, row := range suppressions {
		data[i] = suppressionOf(row)
	}
	return endpoint.List[Suppression]{Data: data}, nil
}

// delete clears a suppression, so mail from the domain to the address is
// tried again.
// DELETE /api/v1/admin/domains/{id}/suppressions/{sid}
func (h *SuppressionHandler) delete(r *http.Request, in suppressionPath) (endpoint.Empty, error) {
	db := h.db.WithContext(r.Context())
	domainID := in.ID
	suppressionID := in.SID

	// Scoped to the domain in the path, which the route checked the caller's
	// capability on: another domain's suppression is not found.
	res := db.Where("id = ? AND domain_id = ?", suppressionID, domainID).Delete(&models.Suppression{})
	if res.Error != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to clear suppression")
	}
	if res.RowsAffected == 0 {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Suppression not found")
	}

	return endpoint.Empty{}, nil
}
