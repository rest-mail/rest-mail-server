package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type AccountHandler struct {
	db *gorm.DB
}

func NewAccountHandler(db *gorm.DB) *AccountHandler {
	return &AccountHandler{db: db}
}

// accountResponse represents a linked account for the frontend.
type accountResponse struct {
	ID          uint   `json:"id"`
	MailboxID   uint   `json:"mailbox_id"`
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	IsPrimary   bool   `json:"is_primary"`
}

// ListAccounts returns linked mailboxes for the logged-in user.
// GET /api/v1/accounts
func (h *AccountHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var account models.WebmailAccount
	if err := h.db.Preload("PrimaryMailbox").Preload("LinkedAccounts.Mailbox").First(&account, claims.WebmailAccountID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Webmail account not found")
		return
	}

	var accounts []accountResponse

	// Primary account
	accounts = append(accounts, accountResponse{
		ID:          account.ID,
		MailboxID:   account.PrimaryMailboxID,
		Address:     account.PrimaryMailbox.Address,
		DisplayName: account.PrimaryMailbox.DisplayName,
		IsPrimary:   true,
	})

	// Linked accounts
	for _, la := range account.LinkedAccounts {
		displayName := la.DisplayName
		if displayName == "" {
			displayName = la.Mailbox.DisplayName
		}
		accounts = append(accounts, accountResponse{
			ID:          la.ID,
			MailboxID:   la.MailboxID,
			Address:     la.Mailbox.Address,
			DisplayName: displayName,
			IsPrimary:   false,
		})
	}

	respond.List(w, accounts, nil)
}

// GetAccount returns details of a specific linked mailbox.
// GET /api/v1/accounts/:id
func (h *AccountHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	// Check primary account first
	var account models.WebmailAccount
	if err := h.db.Preload("PrimaryMailbox").First(&account, id).Error; err == nil {
		if account.ID == claims.WebmailAccountID {
			respond.Data(w, http.StatusOK, accountResponse{
				ID:          account.ID,
				MailboxID:   account.PrimaryMailboxID,
				Address:     account.PrimaryMailbox.Address,
				DisplayName: account.PrimaryMailbox.DisplayName,
				IsPrimary:   true,
			})
			return
		}
	}

	// Check linked accounts
	var linked models.LinkedAccount
	if err := h.db.Preload("Mailbox").Where("webmail_account_id = ? AND id = ?", claims.WebmailAccountID, id).First(&linked).Error; err == nil {
		displayName := linked.DisplayName
		if displayName == "" {
			displayName = linked.Mailbox.DisplayName
		}
		respond.Data(w, http.StatusOK, accountResponse{
			ID:          linked.ID,
			MailboxID:   linked.MailboxID,
			Address:     linked.Mailbox.Address,
			DisplayName: displayName,
			IsPrimary:   false,
		})
		return
	}

	respond.Error(w, http.StatusNotFound, "not_found", "Account not found")
}

// LinkAccount links an additional mailbox to the webmail account.
// POST /api/v1/accounts
func (h *AccountHandler) LinkAccount(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req struct {
		Address     string `json:"address"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Address == "" || req.Password == "" {
		respond.ValidationError(w, map[string]string{
			"address":  "required",
			"password": "required",
		})
		return
	}

	// Verify the mailbox exists and credentials are correct
	var mailbox models.Mailbox
	if err := h.db.Where("address = ? AND active = ?", req.Address, true).First(&mailbox).Error; err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid email or password")
		return
	}

	if err := auth.CheckPassword(req.Password, mailbox.Password); err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid email or password")
		return
	}

	linked := models.LinkedAccount{
		WebmailAccountID: claims.WebmailAccountID,
		MailboxID:        mailbox.ID,
		DisplayName:      req.DisplayName,
	}

	if err := h.db.Create(&linked).Error; err != nil {
		respond.Error(w, http.StatusConflict, "already_exists", "This mailbox is already linked")
		return
	}

	respond.Data(w, http.StatusCreated, accountResponse{
		ID:          linked.ID,
		MailboxID:   mailbox.ID,
		Address:     mailbox.Address,
		DisplayName: req.DisplayName,
		IsPrimary:   false,
	})
}

// TestConnection verifies that an email address and password are valid.
// POST /api/v1/accounts/test-connection
func (h *AccountHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req struct {
		Address  string `json:"address"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Address == "" || req.Password == "" {
		respond.ValidationError(w, map[string]string{
			"address":  "required",
			"password": "required",
		})
		return
	}

	var mailbox models.Mailbox
	if err := h.db.Where("address = ? AND active = ?", req.Address, true).First(&mailbox).Error; err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid email or password")
		return
	}

	if err := auth.CheckPassword(req.Password, mailbox.Password); err != nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid email or password")
		return
	}

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"status":       "ok",
		"address":      mailbox.Address,
		"display_name": mailbox.DisplayName,
	})
}

// UnlinkAccount removes a secondary mailbox link.
// DELETE /api/v1/accounts/:id
func (h *AccountHandler) UnlinkAccount(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var linked models.LinkedAccount
	if err := h.db.Where("webmail_account_id = ? AND id = ?", claims.WebmailAccountID, id).First(&linked).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Linked account not found")
		return
	}

	h.db.Delete(&linked)
	w.WriteHeader(http.StatusNoContent)
}
