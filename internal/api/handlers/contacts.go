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

type ContactHandler struct {
	db *gorm.DB
}

func NewContactHandler(db *gorm.DB) *ContactHandler {
	return &ContactHandler{db: db}
}

// ListContacts returns contacts for a mailbox with optional filtering.
// GET /api/v1/accounts/{id}/contacts
func (h *ContactHandler) ListContacts(w http.ResponseWriter, r *http.Request) {
	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	query := h.db.Where("mailbox_id = ?", accountID)

	if trustLevel := r.URL.Query().Get("trust_level"); trustLevel != "" {
		query = query.Where("trust_level = ?", trustLevel)
	}

	if search := r.URL.Query().Get("search"); search != "" {
		query = query.Where("email LIKE ?", "%"+search+"%")
	}

	var contacts []models.Contact
	if err := query.Order("email ASC").Find(&contacts).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to list contacts")
		return
	}

	respond.List(w, contacts, nil)
}

type createContactRequest struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	TrustLevel string `json:"trust_level"`
}

// CreateContact adds a new contact to a mailbox.
// POST /api/v1/accounts/{id}/contacts
func (h *ContactHandler) CreateContact(w http.ResponseWriter, r *http.Request) {
	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var req createContactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	errors := map[string]string{}
	if req.Email == "" {
		errors["email"] = "required"
	}
	if len(errors) > 0 {
		respond.ValidationError(w, errors)
		return
	}

	trustLevel := req.TrustLevel
	if trustLevel == "" {
		trustLevel = "trusted"
	}

	contact := models.Contact{
		MailboxID:  uint(accountID),
		Email:      req.Email,
		Name:       req.Name,
		TrustLevel: trustLevel,
		Source:     "manual",
	}

	if err := h.db.Create(&contact).Error; err != nil {
		respond.Error(w, http.StatusConflict, "already_exists", "Contact already exists")
		return
	}

	respond.Data(w, http.StatusCreated, contact)
}

type updateContactRequest struct {
	Name       *string `json:"name"`
	TrustLevel *string `json:"trust_level"`
}

// UpdateContact updates an existing contact's fields.
// PATCH /api/v1/accounts/{id}/contacts/{cid}
func (h *ContactHandler) UpdateContact(w http.ResponseWriter, r *http.Request) {
	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	contactID, err := strconv.ParseUint(chi.URLParam(r, "cid"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid contact ID")
		return
	}

	var contact models.Contact
	if err := h.db.Where("id = ? AND mailbox_id = ?", contactID, accountID).First(&contact).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Contact not found")
		return
	}

	var req updateContactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.TrustLevel != nil {
		updates["trust_level"] = *req.TrustLevel
	}

	if len(updates) > 0 {
		if err := h.db.Model(&contact).Updates(updates).Error; err != nil {
			respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to update contact")
			return
		}
	}

	h.db.Where("id = ? AND mailbox_id = ?", contactID, accountID).First(&contact)
	respond.Data(w, http.StatusOK, contact)
}

// DeleteContact removes a contact from a mailbox.
// DELETE /api/v1/accounts/{id}/contacts/{cid}
func (h *ContactHandler) DeleteContact(w http.ResponseWriter, r *http.Request) {
	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	contactID, err := strconv.ParseUint(chi.URLParam(r, "cid"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid contact ID")
		return
	}

	var contact models.Contact
	if err := h.db.Where("id = ? AND mailbox_id = ?", contactID, accountID).First(&contact).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Contact not found")
		return
	}

	if err := h.db.Delete(&contact).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to delete contact")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type blockSenderRequest struct {
	Email string `json:"email"`
}

// BlockSender creates or updates a contact with trust_level="blocked".
// POST /api/v1/accounts/{id}/contacts/block
func (h *ContactHandler) BlockSender(w http.ResponseWriter, r *http.Request) {
	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var req blockSenderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Email == "" {
		respond.ValidationError(w, map[string]string{"email": "required"})
		return
	}

	var contact models.Contact
	result := h.db.Where("mailbox_id = ? AND email = ?", accountID, req.Email).First(&contact)

	if result.Error == nil {
		// Contact exists, update to blocked
		h.db.Model(&contact).Updates(map[string]interface{}{
			"trust_level": "blocked",
			"source":      "manual",
		})
		h.db.First(&contact, contact.ID)
		respond.Data(w, http.StatusOK, contact)
		return
	}

	// Contact does not exist, create as blocked
	contact = models.Contact{
		MailboxID:  uint(accountID),
		Email:      req.Email,
		TrustLevel: "blocked",
		Source:     "manual",
	}

	if err := h.db.Create(&contact).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to block sender")
		return
	}

	respond.Data(w, http.StatusCreated, contact)
}

// SuggestContacts returns autocomplete suggestions for the compose "To" field.
// GET /api/v1/accounts/{id}/contacts/suggest?q=...
func (h *ContactHandler) SuggestContacts(w http.ResponseWriter, r *http.Request) {
	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	q := r.URL.Query().Get("q")
	if q == "" {
		respond.List(w, []models.Contact{}, nil)
		return
	}

	var contacts []models.Contact
	h.db.Where("mailbox_id = ? AND (email LIKE ? OR name LIKE ?) AND trust_level != ?",
		accountID, "%"+q+"%", "%"+q+"%", "blocked").
		Order("name ASC, email ASC").
		Limit(10).
		Find(&contacts)

	respond.List(w, contacts, nil)
}

type importContactsRequest struct {
	Contacts []importContactEntry `json:"contacts"`
}

type importContactEntry struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// ImportContacts bulk-imports contacts for a mailbox.
// POST /api/v1/accounts/{id}/contacts/import
func (h *ContactHandler) ImportContacts(w http.ResponseWriter, r *http.Request) {
	accountID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid account ID")
		return
	}

	var req importContactsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if len(req.Contacts) == 0 {
		respond.ValidationError(w, map[string]string{"contacts": "required"})
		return
	}

	imported := 0
	for _, entry := range req.Contacts {
		if entry.Email == "" {
			continue
		}

		contact := models.Contact{
			MailboxID:  uint(accountID),
			Email:      entry.Email,
			Name:       entry.Name,
			TrustLevel: "auto",
			Source:     "import",
		}

		if err := h.db.Create(&contact).Error; err != nil {
			// Skip duplicates, continue importing
			continue
		}
		imported++
	}

	respond.Data(w, http.StatusOK, map[string]int{"imported": imported})
}
