package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

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

// dkimEntry contains only public monitoring metadata. Unknown legacy key ages
// are null; the domain's CreatedAt/UpdatedAt are not key timestamps.
type dkimEntry struct {
	DomainID   uint       `json:"domain_id"`
	Domain     string     `json:"domain"`
	Selector   string     `json:"selector"`
	HasKey     bool       `json:"has_key"`
	CreatedAt  *time.Time `json:"created_at"`
	AgeSeconds *float64   `json:"age_seconds"`
}

func newDKIMEntry(domain models.Domain, now time.Time) dkimEntry {
	entry := dkimEntry{
		DomainID: domain.ID, Domain: domain.Name, Selector: domain.DKIMSelector,
		HasKey: true, CreatedAt: domain.DKIMCreatedAt,
	}
	if domain.DKIMCreatedAt != nil {
		age := max(0, now.Sub(*domain.DKIMCreatedAt).Seconds())
		entry.AgeSeconds = &age
	}
	return entry
}

// ListKeys returns DKIM configuration for all domains (keys are redacted).
// GET /api/v1/admin/dkim
func (h *DKIMHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	var domains []models.Domain
	if err := h.db.WithContext(r.Context()).Select("id", "name", "dkim_selector", "dkim_created_at").Where("dkim_selector != '' AND dkim_private_key != ''").Find(&domains).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list DKIM keys")
		return
	}

	entries := make([]dkimEntry, 0, len(domains))
	now := time.Now()
	for _, d := range domains {
		entries = append(entries, newDKIMEntry(d, now))
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

	createdAt := domain.DKIMCreatedAt
	// Re-saving the same key must not make it appear younger. Legacy keys
	// re-saved unchanged still have an unknown age.
	previous, loadErr := models.LoadDKIMPrivateKey(domain.DKIMPrivateKey, h.masterKey)
	if loadErr != nil || previous != req.PrivateKey {
		now := time.Now().UTC()
		createdAt = &now
	}
	if err := h.db.Model(&domain).Updates(map[string]interface{}{
		"dkim_selector":    req.Selector,
		"dkim_private_key": keyToStore,
		"dkim_created_at":  createdAt,
	}).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to save the DKIM key")
		return
	}

	domain.DKIMSelector = req.Selector
	domain.DKIMCreatedAt = createdAt
	respond.Data(w, http.StatusOK, newDKIMEntry(domain, time.Now()))
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

	if err := h.db.Model(&domain).Updates(map[string]interface{}{
		"dkim_selector":    "",
		"dkim_private_key": "",
		"dkim_created_at":  nil,
	}).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete the DKIM key")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
