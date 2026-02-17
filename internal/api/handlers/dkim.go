package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type DKIMHandler struct {
	db *gorm.DB
}

func NewDKIMHandler(db *gorm.DB) *DKIMHandler {
	return &DKIMHandler{db: db}
}

// ListKeys returns DKIM configuration for all domains (keys are redacted).
// GET /api/v1/admin/dkim
func (h *DKIMHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	var domains []models.Domain
	if err := h.db.Where("dkim_selector != '' AND dkim_private_key != ''").Find(&domains).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list DKIM keys")
		return
	}

	type dkimEntry struct {
		DomainID uint   `json:"domain_id"`
		Domain   string `json:"domain"`
		Selector string `json:"selector"`
		HasKey   bool   `json:"has_key"`
	}
	var entries []dkimEntry
	for _, d := range domains {
		entries = append(entries, dkimEntry{
			DomainID: d.ID,
			Domain:   d.Name,
			Selector: d.DKIMSelector,
			HasKey:   d.DKIMPrivateKey != "",
		})
	}
	respond.List(w, entries, nil)
}

// SetKey sets or updates the DKIM key for a domain.
// PUT /api/v1/admin/dkim/{id}
func (h *DKIMHandler) SetKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Domain not found")
		return
	}

	var req struct {
		Selector   string `json:"selector"`
		PrivateKey string `json:"private_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Selector == "" || req.PrivateKey == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "selector and private_key are required")
		return
	}

	h.db.Model(&domain).Updates(map[string]interface{}{
		"dkim_selector":    req.Selector,
		"dkim_private_key": req.PrivateKey,
	})

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"domain_id": domain.ID,
		"domain":    domain.Name,
		"selector":  req.Selector,
		"has_key":   true,
	})
}

// DeleteKey removes the DKIM key for a domain.
// DELETE /api/v1/admin/dkim/{id}
func (h *DKIMHandler) DeleteKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid domain ID")
		return
	}

	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Domain not found")
		return
	}

	h.db.Model(&domain).Updates(map[string]interface{}{
		"dkim_selector":    "",
		"dkim_private_key": "",
	})

	w.WriteHeader(http.StatusNoContent)
}
