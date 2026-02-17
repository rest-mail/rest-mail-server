package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"gorm.io/gorm"
)

// VacationConfig stores out-of-office auto-reply settings for a mailbox.
type VacationConfig struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	MailboxID uint       `gorm:"uniqueIndex;not null" json:"mailbox_id"`
	Enabled   bool       `gorm:"default:false" json:"enabled"`
	Subject   string     `gorm:"size:500" json:"subject"`
	Body      string     `gorm:"type:text" json:"body"`
	StartDate *time.Time `json:"start_date,omitempty"`
	EndDate   *time.Time `json:"end_date,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (VacationConfig) TableName() string { return "vacation_configs" }

// VacationHandler manages vacation/out-of-office auto-reply settings.
type VacationHandler struct {
	db *gorm.DB
}

// NewVacationHandler creates a new VacationHandler and auto-migrates the
// vacation_configs table.
func NewVacationHandler(db *gorm.DB) *VacationHandler {
	db.AutoMigrate(&VacationConfig{})
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

// GetVacation returns the vacation auto-reply configuration for a mailbox.
// GET /api/v1/accounts/{id}/vacation
func (h *VacationHandler) GetVacation(w http.ResponseWriter, r *http.Request) {
	mailboxID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var config VacationConfig
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
	mailboxID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
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

	var config VacationConfig
	result := h.db.Where("mailbox_id = ?", mailboxID).First(&config)

	if result.Error != nil {
		// Create new config.
		config = VacationConfig{
			MailboxID: uint(mailboxID),
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
	mailboxID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var config VacationConfig
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
