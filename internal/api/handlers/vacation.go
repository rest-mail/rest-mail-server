package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// VacationHandler manages vacation/out-of-office auto-reply settings.
type VacationHandler struct {
	db *gorm.DB
}

// NewVacationHandler creates a new VacationHandler.
func NewVacationHandler(db *gorm.DB) *VacationHandler {
	return &VacationHandler{db: db}
}

// vacationSetting is the API-facing representation used for request/response
// bodies.
type vacationSetting struct {
	Enabled   bool       `json:"enabled"`
	Subject   string     `json:"subject"`
	Body      string     `json:"body"`
	StartDate *time.Time `json:"start_date,omitempty"`
	EndDate   *time.Time `json:"end_date,omitempty"`
}

// resolveMailboxID resolves the account ID from the URL to a mailbox ID,
// verifying that the authenticated user owns the account (either as their
// primary account or a linked account).
func (h *VacationHandler) resolveMailboxID(r *http.Request, accountIDStr string) (uint, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return 0, fmt.Errorf("no claims")
	}

	accountID, err := strconv.ParseUint(accountIDStr, 10, 32)
	if err != nil {
		return 0, err
	}

	var account models.WebmailAccount
	if err := h.db.First(&account, accountID).Error; err == nil {
		if account.ID == claims.WebmailAccountID {
			return account.PrimaryMailboxID, nil
		}
	}

	var linked models.LinkedAccount
	if err := h.db.Where("webmail_account_id = ? AND id = ?", claims.WebmailAccountID, accountID).First(&linked).Error; err == nil {
		return linked.MailboxID, nil
	}

	return 0, fmt.Errorf("access denied")
}

// GetVacation returns the vacation auto-reply configuration for a mailbox.
// GET /api/v1/accounts/{id}/vacation
func (h *VacationHandler) GetVacation(w http.ResponseWriter, r *http.Request) {
	mailboxID, err := h.resolveMailboxID(r, chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var config models.VacationConfig
	if err := h.db.Where("mailbox_id = ?", mailboxID).First(&config).Error; err != nil {
		// Return defaults when no vacation config exists yet.
		respond.Data(w, http.StatusOK, vacationSetting{
			Enabled: false,
			Subject: "",
			Body:    "",
		})
		return
	}

	respond.Data(w, http.StatusOK, vacationSetting{
		Enabled:   config.Enabled,
		Subject:   config.Subject,
		Body:      config.Body,
		StartDate: config.StartDate,
		EndDate:   config.EndDate,
	})
}

// SetVacation creates or updates the vacation auto-reply configuration.
// PUT /api/v1/accounts/{id}/vacation
func (h *VacationHandler) SetVacation(w http.ResponseWriter, r *http.Request) {
	mailboxID, err := h.resolveMailboxID(r, chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var req vacationSetting
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Validate that subject and body are non-empty when enabling vacation.
	if req.Enabled {
		errs := map[string]string{}
		if req.Subject == "" {
			errs["subject"] = "required when enabled"
		}
		if req.Body == "" {
			errs["body"] = "required when enabled"
		}
		if len(errs) > 0 {
			respond.ValidationError(w, errs)
			return
		}
	}

	// Validate that end_date is after start_date when both are provided.
	if req.StartDate != nil && req.EndDate != nil {
		if req.EndDate.Before(*req.StartDate) {
			respond.ValidationError(w, map[string]string{
				"end_date": "must be after start_date",
			})
			return
		}
	}

	var config models.VacationConfig
	result := h.db.Where("mailbox_id = ?", mailboxID).First(&config)

	if result.Error != nil {
		// Create new config.
		config = models.VacationConfig{
			MailboxID: mailboxID,
			Enabled:   req.Enabled,
			Subject:   req.Subject,
			Body:      req.Body,
			StartDate: req.StartDate,
			EndDate:   req.EndDate,
		}
		if err := h.db.Create(&config).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to create vacation config")
			return
		}
	} else {
		// Update existing config.
		updates := map[string]interface{}{
			"enabled":    req.Enabled,
			"subject":    req.Subject,
			"body":       req.Body,
			"start_date": req.StartDate,
			"end_date":   req.EndDate,
		}
		if err := h.db.Model(&config).Updates(updates).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update vacation config")
			return
		}
	}

	respond.Data(w, http.StatusOK, vacationSetting{
		Enabled:   req.Enabled,
		Subject:   req.Subject,
		Body:      req.Body,
		StartDate: req.StartDate,
		EndDate:   req.EndDate,
	})
}

// DisableVacation turns off the vacation auto-reply for a mailbox.
// DELETE /api/v1/accounts/{id}/vacation
func (h *VacationHandler) DisableVacation(w http.ResponseWriter, r *http.Request) {
	mailboxID, err := h.resolveMailboxID(r, chi.URLParam(r, "id"))
	if err != nil {
		respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
		return
	}

	var config models.VacationConfig
	if err := h.db.Where("mailbox_id = ?", mailboxID).First(&config).Error; err != nil {
		// Nothing to disable; return success.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.db.Model(&config).Update("enabled", false).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to disable vacation config")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
