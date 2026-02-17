package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

// SieveHandler manages per-mailbox Sieve filter scripts.
type SieveHandler struct {
	db *gorm.DB
}

// NewSieveHandler creates a new SieveHandler.
func NewSieveHandler(db *gorm.DB) *SieveHandler {
	return &SieveHandler{db: db}
}

// GetScript returns the Sieve script for a mailbox.
// GET /api/v1/accounts/{id}/sieve
func (h *SieveHandler) GetScript(w http.ResponseWriter, r *http.Request) {
	mailboxID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var script models.SieveScript
	if err := h.db.Where("mailbox_id = ?", mailboxID).First(&script).Error; err != nil {
		// Return empty script if none exists
		respond.Data(w, http.StatusOK, map[string]interface{}{
			"mailbox_id": mailboxID,
			"script":     "",
			"active":     false,
		})
		return
	}

	respond.Data(w, http.StatusOK, script)
}

// PutScript creates or updates the Sieve script for a mailbox.
// PUT /api/v1/accounts/{id}/sieve
func (h *SieveHandler) PutScript(w http.ResponseWriter, r *http.Request) {
	mailboxID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var req struct {
		Script string `json:"script"`
		Active *bool  `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Validate the script syntax
	if req.Script != "" {
		if err := filters.ValidateSieve(req.Script); err != nil {
			respond.ValidationError(w, map[string]string{
				"script": err.Error(),
			})
			return
		}
	}

	active := true
	if req.Active != nil {
		active = *req.Active
	}

	// Upsert
	var script models.SieveScript
	result := h.db.Where("mailbox_id = ?", mailboxID).First(&script)
	if result.Error != nil {
		// Create new
		script = models.SieveScript{
			MailboxID: uint(mailboxID),
			Script:    req.Script,
			Active:    active,
		}
		if err := h.db.Create(&script).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create script")
			return
		}
		respond.Data(w, http.StatusCreated, script)
		return
	}

	// Update existing
	script.Script = req.Script
	script.Active = active
	if err := h.db.Save(&script).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update script")
		return
	}

	respond.Data(w, http.StatusOK, script)
}

// DeleteScript removes the Sieve script for a mailbox.
// DELETE /api/v1/accounts/{id}/sieve
func (h *SieveHandler) DeleteScript(w http.ResponseWriter, r *http.Request) {
	mailboxID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	h.db.Where("mailbox_id = ?", mailboxID).Delete(&models.SieveScript{})
	respond.Data(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ValidateScript checks a Sieve script without saving it.
// POST /api/v1/accounts/{id}/sieve/validate
func (h *SieveHandler) ValidateScript(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Script string `json:"script"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Script == "" {
		respond.Data(w, http.StatusOK, map[string]interface{}{
			"valid":   true,
			"message": "Empty script is valid",
		})
		return
	}

	if err := filters.ValidateSieve(req.Script); err != nil {
		respond.Data(w, http.StatusOK, map[string]interface{}{
			"valid":   false,
			"message": err.Error(),
		})
		return
	}

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"valid":   true,
		"message": "Script is valid",
	})
}
