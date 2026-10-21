package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type dkimRotationSettings struct {
	DomainID             uint `json:"domain_id"`
	RotationIntervalDays int  `json:"rotation_interval_days"`
}

// rotationDomain loads only public configuration, never the signing key.
func (h *DKIMHandler) rotationDomain(w http.ResponseWriter, r *http.Request) (*models.Domain, bool) {
	id, err := pathID(r, "id")
	if err != nil || id == 0 {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return nil, false
	}
	var domain models.Domain
	err = h.db.WithContext(r.Context()).Select("id", "dkim_rotation_interval_days").First(&domain, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respond.Error(w, http.StatusNotFound, "not_found", "Domain not found")
		return nil, false
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to load DKIM rotation settings")
		return nil, false
	}
	return &domain, true
}

// GetRotation returns the requested interval, even for a domain with no key.
// GET /api/v1/admin/dkim/{id}/rotation
func (h *DKIMHandler) GetRotation(w http.ResponseWriter, r *http.Request) {
	domain, ok := h.rotationDomain(w, r)
	if !ok {
		return
	}
	respond.Data(w, http.StatusOK, dkimRotationSettings{domain.ID, domain.DKIMRotationIntervalDays})
}

// SetRotation records policy for the rotation worker without changing the
// current signing key, selector or installation time.
// PUT /api/v1/admin/dkim/{id}/rotation
func (h *DKIMHandler) SetRotation(w http.ResponseWriter, r *http.Request) {
	domain, ok := h.rotationDomain(w, r)
	if !ok {
		return
	}
	var req struct {
		RotationIntervalDays *int `json:"rotation_interval_days"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Expected one JSON object")
		return
	}
	if req.RotationIntervalDays == nil || *req.RotationIntervalDays < 0 || *req.RotationIntervalDays > models.MaxDKIMRotationIntervalDays {
		message := fmt.Sprintf("Must be an integer between 0 and %d days; 0 disables rotation", models.MaxDKIMRotationIntervalDays)
		respond.JSON(w, http.StatusUnprocessableEntity, respond.ErrorResponse{Error: respond.ErrorBody{
			Code: "validation_failed", Message: "Invalid DKIM rotation interval",
			Details: map[string]any{"fields": map[string]string{"rotation_interval_days": message}},
		}})
		return
	}
	result := h.db.WithContext(r.Context()).Model(&models.Domain{}).Where("id = ?", domain.ID).
		Update("dkim_rotation_interval_days", *req.RotationIntervalDays)
	if result.Error != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save DKIM rotation settings")
		return
	}
	if result.RowsAffected == 0 {
		respond.Error(w, http.StatusNotFound, "not_found", "Domain not found")
		return
	}
	respond.Data(w, http.StatusOK, dkimRotationSettings{domain.ID, *req.RotationIntervalDays})
}
