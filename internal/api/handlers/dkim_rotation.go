package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dkimrotate"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
)

type dkimRotationSettings struct {
	DomainID             uint `json:"domain_id"`
	RotationIntervalDays int  `json:"rotation_interval_days" minimum:"0" maximum:"3650"`
	// State is where the domain's key rotation is (#728): stable, with no rotation under
	// way; awaiting_publication, a staged key whose record has not resolved yet;
	// waiting_ttl, published and waiting one TTL before signing moves to it; retiring,
	// signing has moved and the previous key's record stays until it retires.
	State string `json:"state" enums:"stable,awaiting_publication,waiting_ttl,retiring"`
	// Staged is the key signing moves to next, null when there is none.
	Staged *dkimRotationKey `json:"staged"`
	// Previous is the key signing moved from, null when there is none.
	Previous *dkimRotationKey `json:"previous"`
}

// dkimRotationKey is a key in rotation as an administrator acts on it: the TXT record to
// publish for it, and when the rotation moves on.
type dkimRotationKey struct {
	Selector string `json:"selector"`
	// RecordName and RecordValue are the TXT record to publish; the value is empty when
	// the stored key cannot be read.
	RecordName  string `json:"record_name"`
	RecordValue string `json:"record_value"`
	// PublishedAt is when the record was first seen to resolve, null until then.
	PublishedAt *time.Time `json:"published_at"`
	// ActivatesAt, for a published staged key, is when signing moves to it.
	ActivatesAt *time.Time `json:"activates_at,omitempty"`
	// RetireAfter, for the previous key, is when its record can be removed.
	RetireAfter *time.Time `json:"retire_after,omitempty"`
}

// rotationStatus fills in where domain's rotation is, from its staged and previous keys.
func (h *DKIMHandler) rotationStatus(r *http.Request, domain models.Domain, settings *dkimRotationSettings) error {
	var keys []models.DKIMRotationKey
	if err := h.db.WithContext(r.Context()).Where("domain_id = ?", domain.ID).Find(&keys).Error; err != nil {
		return err
	}
	name, err := rmail.DomainASCII(domain.Name)
	if err != nil {
		name = domain.Name
	}
	settings.State = "stable"
	for _, key := range keys {
		status := &dkimRotationKey{
			Selector:    key.Selector,
			RecordName:  key.Selector + "._domainkey." + name,
			PublishedAt: key.PublishedAt,
		}
		if value, err := dkimrotate.PublicRecord(key.PrivateKey, h.masterKey); err == nil {
			status.RecordValue = value
		}
		switch key.Slot {
		case models.DKIMRotationStaged:
			if key.PublishedAt != nil {
				at := key.PublishedAt.Add(time.Duration(key.TTLSeconds) * time.Second)
				status.ActivatesAt = &at
			}
			settings.Staged = status
		case models.DKIMRotationPrevious:
			status.RetireAfter = key.RetireAfter
			settings.Previous = status
		}
	}
	switch {
	case settings.Staged != nil && settings.Staged.PublishedAt == nil:
		settings.State = "awaiting_publication"
	case settings.Staged != nil:
		settings.State = "waiting_ttl"
	case settings.Previous != nil:
		settings.State = "retiring"
	}
	return nil
}

// rotationDomain loads only public configuration, never the signing key.
func (h *DKIMHandler) rotationDomain(r *http.Request, id uint32) (models.Domain, error) {
	var domain models.Domain
	err := h.db.WithContext(r.Context()).Select("id", "name", "dkim_rotation_interval_days").First(&domain, id).Error
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
	settings := dkimRotationSettings{DomainID: domain.ID, RotationIntervalDays: domain.DKIMRotationIntervalDays}
	if err := h.rotationStatus(r, domain, &settings); err != nil {
		return dkimRotationSettings{}, err
	}
	return settings, nil
}

type setDKIMRotationBody struct {
	RotationIntervalDays int `json:"rotation_interval_days" minimum:"0" maximum:"3650" message:"Must be an integer between 0 and 3650 days; 0 disables rotation"`
}
type setDKIMRotationRequest struct {
	ID   uint32              `path:"id" minimum:"1" example:"1"`
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
	settings := dkimRotationSettings{DomainID: domain.ID, RotationIntervalDays: in.Body.RotationIntervalDays}
	if err := h.rotationStatus(r, domain, &settings); err != nil {
		return dkimRotationSettings{}, err
	}
	return settings, nil
}
