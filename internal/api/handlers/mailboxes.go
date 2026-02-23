package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type MailboxHandler struct {
	db *gorm.DB
}

func NewMailboxHandler(db *gorm.DB) *MailboxHandler {
	return &MailboxHandler{db: db}
}

func (h *MailboxHandler) List(w http.ResponseWriter, r *http.Request) {
	var mailboxes []models.Mailbox
	query := h.db.Preload("Domain").Preload("QuotaUsage")

	if domainID := r.URL.Query().Get("domain_id"); domainID != "" {
		query = query.Where("domain_id = ?", domainID)
	}

	if err := query.Order("address ASC").Find(&mailboxes).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list mailboxes")
		return
	}
	respond.List(w, mailboxes, nil)
}

type createMailboxRequest struct {
	Address     string `json:"address"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	QuotaBytes  *int64 `json:"quota_bytes"`
}

func (h *MailboxHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createMailboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	errs := map[string]string{}
	if req.Address == "" {
		errs["address"] = "required"
	}
	if req.Password == "" {
		errs["password"] = "required"
	} else if len(req.Password) < 8 {
		errs["password"] = "must be at least 8 characters"
	}
	if len(errs) > 0 {
		respond.ValidationError(w, errs)
		return
	}

	parts := strings.SplitN(req.Address, "@", 2)
	if len(parts) != 2 {
		respond.ValidationError(w, map[string]string{"address": "must be a valid email address"})
		return
	}
	localPart := parts[0]
	domainName := parts[1]

	var domain models.Domain
	if err := h.db.Where("name = ?", domainName).First(&domain).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "domain_not_found", "Domain '"+domainName+"' not found")
		return
	}

	hashedPassword, err := auth.HashPassword(req.Password)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to hash password")
		return
	}

	mailbox := models.Mailbox{
		DomainID:    domain.ID,
		LocalPart:   localPart,
		Address:     req.Address,
		Password:    hashedPassword,
		DisplayName: req.DisplayName,
		Active:      true,
	}

	if req.QuotaBytes != nil {
		mailbox.QuotaBytes = *req.QuotaBytes
	} else {
		mailbox.QuotaBytes = domain.DefaultQuotaBytes
	}

	if err := h.db.Create(&mailbox).Error; err != nil {
		respond.Error(w, http.StatusConflict, "already_exists", "Mailbox already exists")
		return
	}

	// Create quota usage record
	h.db.Create(&models.QuotaUsage{MailboxID: mailbox.ID})

	respond.Data(w, http.StatusCreated, mailbox)
}

func (h *MailboxHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid mailbox ID")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.Preload("Domain").Preload("QuotaUsage").First(&mailbox, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		return
	}

	respond.Data(w, http.StatusOK, mailbox)
}

type updateMailboxRequest struct {
	Password    *string `json:"password"`
	DisplayName *string `json:"display_name"`
	QuotaBytes  *int64  `json:"quota_bytes"`
	Active      *bool   `json:"active"`
}

func (h *MailboxHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid mailbox ID")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.First(&mailbox, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		return
	}

	var req updateMailboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.Password != nil {
		if len(*req.Password) < 8 {
			respond.ValidationError(w, map[string]string{"password": "must be at least 8 characters"})
			return
		}
		hashed, err := auth.HashPassword(*req.Password)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to hash password")
			return
		}
		updates["password"] = hashed
	}
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.QuotaBytes != nil {
		updates["quota_bytes"] = *req.QuotaBytes
	}
	if req.Active != nil {
		updates["active"] = *req.Active
	}

	if len(updates) > 0 {
		if err := h.db.Model(&mailbox).Updates(updates).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update mailbox")
			return
		}
	}

	h.db.Preload("Domain").Preload("QuotaUsage").First(&mailbox, id)
	respond.Data(w, http.StatusOK, mailbox)
}

func (h *MailboxHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid mailbox ID")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.First(&mailbox, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		return
	}

	if err := h.db.Delete(&mailbox).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete mailbox")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CheckAddress verifies a recipient exists: GET /api/mailboxes?address=...
func (h *MailboxHandler) CheckAddress(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "address query parameter required")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.Where("address = ? AND active = ?", address, true).First(&mailbox).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "mailbox_not_found", "No mailbox found with address "+address)
		return
	}

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"exists":     true,
		"mailbox_id": mailbox.ID,
		"address":    mailbox.Address,
	})
}
