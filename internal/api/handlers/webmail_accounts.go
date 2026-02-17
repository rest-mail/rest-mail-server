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

type WebmailAccountHandler struct {
	db *gorm.DB
}

func NewWebmailAccountHandler(db *gorm.DB) *WebmailAccountHandler {
	return &WebmailAccountHandler{db: db}
}

// List returns all webmail accounts (admin).
func (h *WebmailAccountHandler) List(w http.ResponseWriter, r *http.Request) {
	var accounts []models.WebmailAccount
	if err := h.db.Preload("PrimaryMailbox").Preload("LinkedAccounts.Mailbox").Find(&accounts).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list webmail accounts")
		return
	}
	respond.List(w, accounts, nil)
}

// Create creates a webmail account for an existing mailbox (admin).
func (h *WebmailAccountHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PrimaryMailboxID uint `json:"primary_mailbox_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.PrimaryMailboxID == 0 {
		respond.ValidationError(w, map[string]string{"primary_mailbox_id": "required"})
		return
	}

	// Verify mailbox exists
	var mailbox models.Mailbox
	if err := h.db.First(&mailbox, req.PrimaryMailboxID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "mailbox_not_found", "Mailbox not found")
		return
	}

	account := models.WebmailAccount{
		PrimaryMailboxID: req.PrimaryMailboxID,
	}
	if err := h.db.Create(&account).Error; err != nil {
		respond.Error(w, http.StatusConflict, "already_exists", "Webmail account already exists for this mailbox")
		return
	}

	h.db.Preload("PrimaryMailbox").First(&account, account.ID)
	respond.Data(w, http.StatusCreated, account)
}

// Get returns a specific webmail account (admin).
func (h *WebmailAccountHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var account models.WebmailAccount
	if err := h.db.Preload("PrimaryMailbox").Preload("LinkedAccounts.Mailbox").First(&account, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Webmail account not found")
		return
	}

	respond.Data(w, http.StatusOK, account)
}

// Delete deletes a webmail account (admin).
func (h *WebmailAccountHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var account models.WebmailAccount
	if err := h.db.First(&account, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Webmail account not found")
		return
	}

	h.db.Delete(&account)
	w.WriteHeader(http.StatusNoContent)
}
