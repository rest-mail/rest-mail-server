package handlers

import (
	"errors"
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type dkimRotationSettings struct {
	DomainID             uint `json:"domain_id"`
	RotationIntervalDays int  `json:"rotation_interval_days" minimum:"0" maximum:"3650"`
}

// rotationDomain loads only public configuration, never the signing key.
func (h *DKIMHandler) rotationDomain(r *http.Request, id uint32) (models.Domain, error) {
	var domain models.Domain
	err := h.db.WithContext(r.Context()).Select("id", "dkim_rotation_interval_days").First(&domain, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
	}
	return domain, err
}

func (h *DKIMHandler) getRotation(r *http.Request, in domainPath) (dkimRotationSettings, error) {
	domain, err := h.rotationDomain(r, in.ID)
	if err != nil {
		return dkimRotationSettings{}, err
	}
	return dkimRotationSettings{domain.ID, domain.DKIMRotationIntervalDays}, nil
}

type setDKIMRotationBody struct {
	RotationIntervalDays int `json:"rotation_interval_days" minimum:"0" maximum:"3650" message:"Must be an integer between 0 and 3650 days; 0 disables rotation"`
}
type setDKIMRotationRequest struct {
	ID   uint32              `path:"id" minimum:"1"`
	Body setDKIMRotationBody `body:""`
}

// setRotation changes policy without changing signing material or its installation time.
func (h *DKIMHandler) setRotation(r *http.Request, in setDKIMRotationRequest) (dkimRotationSettings, error) {
	domain, err := h.rotationDomain(r, in.ID)
	if err != nil {
		return dkimRotationSettings{}, err
	}
	result := h.db.WithContext(r.Context()).Model(&models.Domain{}).Where("id = ?", domain.ID).
		Update("dkim_rotation_interval_days", in.Body.RotationIntervalDays)
	if result.Error != nil {
		return dkimRotationSettings{}, result.Error
	}
	if result.RowsAffected == 0 {
		return dkimRotationSettings{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Domain not found")
	}
	return dkimRotationSettings{domain.ID, in.Body.RotationIntervalDays}, nil
}
