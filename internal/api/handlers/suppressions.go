package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
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

// List returns a domain's suppressions that haven't expired, newest first. An
// expired one no longer suppresses, so it isn't listed.
// GET /api/v1/admin/domains/{id}/suppressions
func (h *SuppressionHandler) List(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil || domainID == 0 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var suppressions []models.Suppression
	if err := h.db.Where("domain_id = ? AND expires_at > ?", domainID, time.Now()).
		Order("updated_at DESC").
		Find(&suppressions).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list suppressions")
		return
	}

	respond.List(w, suppressions, nil)
}

// Delete clears a suppression, so mail from the domain to the address is
// tried again.
// DELETE /api/v1/admin/domains/{id}/suppressions/{sid}
func (h *SuppressionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	domainID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil || domainID == 0 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}
	suppressionID, err := strconv.ParseUint(chi.URLParam(r, "sid"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid suppression ID")
		return
	}

	// Scoped to the domain in the path, which the route checked the caller's
	// capability on: another domain's suppression is not found.
	res := h.db.Where("id = ? AND domain_id = ?", suppressionID, domainID).Delete(&models.Suppression{})
	if res.Error != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to clear suppression")
		return
	}
	if res.RowsAffected == 0 {
		respond.Error(w, http.StatusNotFound, "not_found", "Suppression not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
