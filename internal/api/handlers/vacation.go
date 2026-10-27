package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
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

// forRequest gives the handler's SQL work — the access check included — the
// HTTP request's budget and cancellation without changing the shared handler.
func (h *VacationHandler) forRequest(r *http.Request) *VacationHandler {
	if h.db == nil {
		return h
	}
	return &VacationHandler{db: h.db.WithContext(r.Context())}
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

// setVacationRequest replaces the account's vacation reply.
type setVacationRequest struct {
	ID   uint32          `path:"id" minimum:"1" example:"1"`
	Body setVacationBody `body:""`
}

// setVacationBody is the vacation reply to set. enabled is required, so that leaving it out
// is told apart from turning the reply off; subject and body are needed when it is on, and
// end_date may not be before start_date. Those are checked in the handler, with their wording.
type setVacationBody struct {
	Enabled   *bool      `json:"enabled"`
	Subject   string     `json:"subject,omitempty"`
	Body      string     `json:"body,omitempty"`
	StartDate *time.Time `json:"start_date,omitempty"`
	EndDate   *time.Time `json:"end_date,omitempty"`
}

// mailRulesSchemes are the schemes the mail routes take; the handlers check the account is
// reachable with mail:read to read its rules and mail:write to change them.
var mailRulesSchemes = []string{"CookieAuth", "BearerAuth", "APIKeyAuth", "BasicAuth"}

var errMailRulesAccess = endpoint.Refuse(http.StatusForbidden, "forbidden", "Access denied")

// Register mounts the account's vacation reply.
func (h *VacationHandler) Register(rs endpoint.Routes) {
	tags := []string{"Vacation"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/vacation",
		OperationID: "getVacation", Summary: "Get vacation auto-reply settings", Tags: tags,
		Errors: []int{http.StatusForbidden}, Security: handlerChecked(models.CapMailRead, mailRulesSchemes...)}, h.get)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/accounts/{id}/vacation",
		OperationID: "setVacation", Summary: "Set vacation auto-reply settings", Tags: tags,
		Errors: []int{http.StatusForbidden}, Security: handlerChecked(models.CapMailWrite, mailRulesSchemes...)}, h.set)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/accounts/{id}/vacation",
		OperationID: "disableVacation", Summary: "Disable vacation auto-reply", Tags: tags, Status: http.StatusNoContent,
		Errors: []int{http.StatusForbidden}, Security: handlerChecked(models.CapMailWrite, mailRulesSchemes...)}, h.disable)
}

// mailRulesMailbox resolves the account the path names to its mailbox, verifying that the
// session may reach it (its own, or one it holds a mail grant on). Without this check the
// {id} path segment was trusted as a raw mailbox ID, letting any authenticated caller read,
// overwrite or delete another mailbox's rules (IDOR).
func mailRulesMailbox(db *gorm.DB, r *http.Request, accountID uint32) (uint, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return 0, errMailRulesAccess
	}
	mailboxID, err := resolveAccountMailbox(db, uint(accountID), claims, mailCapabilityFor(r))
	if err != nil {
		if errors.Is(err, errNoAccess) {
			return 0, errMailRulesAccess
		}
		return 0, err
	}
	return mailboxID, nil
}

// get returns the vacation auto-reply configuration for a mailbox.
// GET /api/v1/accounts/{id}/vacation
func (h *VacationHandler) get(r *http.Request, in accountPath) (vacationSetting, error) {
	h = h.forRequest(r)
	mailboxID, err := mailRulesMailbox(h.db, r, in.ID)
	if err != nil {
		return vacationSetting{}, err
	}

	var config models.VacationConfig
	if err := h.db.Where("mailbox_id = ?", mailboxID).First(&config).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Return defaults only when no vacation config exists yet.
			return vacationSetting{}, nil
		}
		return vacationSetting{}, err
	}

	return vacationSetting{
		Enabled:   config.Enabled,
		Subject:   config.Subject,
		Body:      config.Body,
		StartDate: config.StartDate,
		EndDate:   config.EndDate,
	}, nil
}

// set creates or updates the vacation auto-reply configuration.
// PUT /api/v1/accounts/{id}/vacation
func (h *VacationHandler) set(r *http.Request, in setVacationRequest) (vacationSetting, error) {
	h = h.forRequest(r)
	mailboxID, err := mailRulesMailbox(h.db, r, in.ID)
	if err != nil {
		return vacationSetting{}, err
	}

	// Enabled is a pointer so an omitted or null "enabled" is told apart from false.
	if in.Body.Enabled == nil {
		return vacationSetting{}, endpoint.Invalid(map[string]string{"enabled": "required"})
	}
	req := vacationSetting{
		Enabled:   *in.Body.Enabled,
		Subject:   in.Body.Subject,
		Body:      in.Body.Body,
		StartDate: in.Body.StartDate,
		EndDate:   in.Body.EndDate,
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
			return vacationSetting{}, endpoint.Invalid(errs)
		}
	}

	// Validate that end_date is after start_date when both are provided.
	if req.StartDate != nil && req.EndDate != nil {
		if req.EndDate.Before(*req.StartDate) {
			return vacationSetting{}, endpoint.Invalid(map[string]string{
				"end_date": "must be after start_date",
			})
		}
	}

	var config models.VacationConfig
	result := h.db.WithContext(r.Context()).Where("mailbox_id = ?", mailboxID).First(&config)

	if result.Error != nil {
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return vacationSetting{}, fmt.Errorf("look up vacation config: %w", result.Error)
		}
		// Create new config.
		config = models.VacationConfig{
			MailboxID: mailboxID,
			Enabled:   req.Enabled,
			Subject:   req.Subject,
			Body:      req.Body,
			StartDate: req.StartDate,
			EndDate:   req.EndDate,
		}
		if err := h.db.WithContext(r.Context()).Create(&config).Error; err != nil {
			return vacationSetting{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to create vacation config")
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
		if err := h.db.WithContext(r.Context()).Model(&config).Updates(updates).Error; err != nil {
			return vacationSetting{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to update vacation config")
		}
	}

	return req, nil
}

// disable turns off the vacation auto-reply for a mailbox.
// DELETE /api/v1/accounts/{id}/vacation
func (h *VacationHandler) disable(r *http.Request, in accountPath) (endpoint.Empty, error) {
	h = h.forRequest(r)
	mailboxID, err := mailRulesMailbox(h.db, r, in.ID)
	if err != nil {
		return endpoint.Empty{}, err
	}

	var config models.VacationConfig
	if err := h.db.WithContext(r.Context()).Where("mailbox_id = ?", mailboxID).First(&config).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Nothing to disable; return success.
			return endpoint.Empty{}, nil
		}
		return endpoint.Empty{}, fmt.Errorf("look up vacation config to disable: %w", err)
	}

	if err := h.db.WithContext(r.Context()).Model(&config).Update("enabled", false).Error; err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to disable vacation config")
	}

	return endpoint.Empty{}, nil
}
