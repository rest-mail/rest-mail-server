package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
)

type DKIMHandler struct {
	db        *gorm.DB
	masterKey string
}

func NewDKIMHandler(db *gorm.DB, masterKey string) *DKIMHandler {
	return &DKIMHandler{db: db, masterKey: masterKey}
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

// SetKey sets or updates the DKIM key for a domain. The key must be one the
// domain can sign with (outbound.CheckDKIMKey); a refusal is 422, its message
// saying which check failed.
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
	// A key or selector go-dkim will not sign with is refused here, not found
	// when the domain's mail is deferred because it cannot be signed (#391).
	if err := outbound.CheckDKIMKey(domain.Name, req.Selector, req.PrivateKey); err != nil {
		var refused *outbound.DKIMKeyError
		if !errors.As(err, &refused) {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to check the DKIM key")
			return
		}
		respond.JSON(w, http.StatusUnprocessableEntity, respond.ErrorResponse{Error: respond.ErrorBody{
			Code:    "validation_failed",
			Message: refused.Error(),
			Details: map[string]interface{}{"fields": map[string]string{refused.Field: refused.Reason}},
		}})
		return
	}

	if h.masterKey == "" {
		slog.Warn("DKIM private key stored in plaintext: MASTER_KEY not configured")
	}
	// OSI-8: encrypt at rest with a version-tagged (dkim:v1:) ciphertext, the
	// only form the signer reads when a MASTER_KEY is set; it fails closed on
	// anything else. With no MASTER_KEY (dev) this returns the plaintext.
	keyToStore, err := models.EncryptDKIMPrivateKey(req.PrivateKey, h.masterKey)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to encrypt private key")
		return
	}

	if err := h.db.Model(&domain).Updates(map[string]interface{}{
		"dkim_selector":    req.Selector,
		"dkim_private_key": keyToStore,
	}).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save the DKIM key")
		return
	}

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
