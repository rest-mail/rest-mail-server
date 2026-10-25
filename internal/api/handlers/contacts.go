package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ContactHandler struct {
	db *gorm.DB
}

func NewContactHandler(db *gorm.DB) *ContactHandler {
	return &ContactHandler{db: db}
}

// Contact is an address an account's mail is filtered by: trusted, blocked, or learned from
// what it sent.
type Contact struct {
	ID         uint      `json:"id"`
	MailboxID  uint      `json:"mailbox_id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	TrustLevel string    `json:"trust_level" enums:"auto,trusted,blocked"`
	Source     string    `json:"source" enums:"sent,manual,import"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func contactOf(m models.Contact) Contact {
	return Contact{
		ID:         m.ID,
		MailboxID:  m.MailboxID,
		Email:      m.Email,
		Name:       m.Name,
		TrustLevel: m.TrustLevel,
		Source:     m.Source,
		CreatedAt:  m.CreatedAt,
		UpdatedAt:  m.UpdatedAt,
	}
}

func contactsOf(rows []models.Contact) []Contact {
	out := make([]Contact, len(rows))
	for i, row := range rows {
		out[i] = contactOf(row)
	}
	return out
}

// ContactsImported is how many of an import's contacts were added; the rest were already
// contacts.
type ContactsImported struct {
	Imported int `json:"imported"`
}

// contactPath names one of the account's contacts.
type contactPath struct {
	ID  uint32 `path:"id" minimum:"1" example:"1"`
	CID uint32 `path:"cid" minimum:"1" example:"1"`
}

// RegisterContacts mounts the account's contacts. Their routes take what the mail routes take;
// the handler checks the account is reachable with mail:read to read and mail:write to change,
// and answers 403 forbidden when it is not.
func (h *ContactHandler) RegisterContacts(rs endpoint.Routes) {
	schemes := []string{"CookieAuth", "BearerAuth", "APIKeyAuth", "BasicAuth"}
	reads := handlerChecked(models.CapMailRead, schemes...)
	writes := handlerChecked(models.CapMailWrite, schemes...)
	tags := []string{"Contacts"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/contacts",
		OperationID: "listContacts", Summary: "List contacts for an account", Tags: tags,
		Errors: []int{http.StatusForbidden}, Security: reads}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/contacts",
		OperationID: "createContact", Summary: "Create a new contact", Tags: tags, Status: http.StatusCreated,
		Errors: []int{http.StatusForbidden, http.StatusConflict}, Security: writes}, h.create)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPatch, Path: "/api/v1/accounts/{id}/contacts/{cid}",
		OperationID: "updateContact", Summary: "Update a contact", Tags: tags,
		Errors: []int{http.StatusForbidden, http.StatusNotFound}, Security: writes}, h.update)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/accounts/{id}/contacts/{cid}",
		OperationID: "deleteContact", Summary: "Delete a contact", Tags: tags, Status: http.StatusNoContent,
		Errors: []int{http.StatusForbidden, http.StatusNotFound}, Security: writes}, h.delete)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/contacts/block",
		OperationID: "blockSender", Summary: "Block a sender", Tags: tags,
		Description: "201 when the sender was not a contact (a new contact, blocked), 200 when an existing contact was changed to blocked.",
		Status:      http.StatusCreated, SuccessStatuses: []int{http.StatusOK},
		Errors: []int{http.StatusForbidden}, Security: writes}, h.block)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/contacts/import",
		OperationID: "importContacts", Summary: "Bulk import contacts", Tags: tags,
		Errors: []int{http.StatusForbidden}, Security: writes}, h.importContacts)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/contacts/suggest",
		OperationID: "suggestContacts", Summary: "Autocomplete contacts by query", Tags: tags,
		Description: "At most 10 contacts whose address or name contains q, blocked ones left out; an empty q answers none.",
		Errors:      []int{http.StatusForbidden}, Security: reads}, h.suggest)
}

var (
	errContactAccess   = endpoint.Refuse(http.StatusForbidden, "forbidden", "Access denied")
	errContactNotFound = endpoint.Refuse(http.StatusNotFound, "not_found", "Contact not found")
)

// resolveMailboxID checks the caller may act on the account (mailbox) the
// URL names with capability, and returns its id.
func (h *ContactHandler) resolveMailboxID(r *http.Request, accountID uint32, capability string) (uint, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return 0, errContactAccess
	}
	mailboxID, err := resolveAccountMailbox(h.db, uint(accountID), claims, capability)
	if err != nil {
		if errors.Is(err, errNoAccess) {
			return 0, errContactAccess
		}
		return 0, fmt.Errorf("resolve contacts account: %w", err)
	}
	return mailboxID, nil
}

type listContactsRequest struct {
	ID         uint32  `path:"id" minimum:"1" example:"1"`
	TrustLevel *string `query:"trust_level" enums:"auto,trusted,blocked" message:"must be one of auto, trusted, blocked"`
	Search     string  `query:"search"`
	Limit      *int    `query:"limit"`
	Offset     *int    `query:"offset"`
}

// list returns contacts for a mailbox with optional filtering.
// GET /api/v1/accounts/{id}/contacts
func (h *ContactHandler) list(r *http.Request, in listContactsRequest) (endpoint.List[Contact], error) {
	mailboxID, err := h.resolveMailboxID(r, in.ID, models.CapMailRead)
	if err != nil {
		return endpoint.List[Contact]{}, err
	}

	query := h.db.Model(&models.Contact{}).Where("mailbox_id = ?", mailboxID)

	if in.TrustLevel != nil {
		query = query.Where("trust_level = ?", *in.TrustLevel)
	}

	// Escape LIKE metacharacters so a search term containing % or _ matches
	// literally instead of acting as a wildcard.
	if in.Search != "" {
		query = query.Where("email LIKE ? ESCAPE '\\'", "%"+escapeLike(in.Search)+"%")
	}

	limit, offset := pagination(in.Limit, in.Offset, defaultListLimit, maxListLimit)

	var total int64
	query.Count(&total)

	var contacts []models.Contact
	if err := query.Order("email ASC").Limit(limit).Offset(offset).Find(&contacts).Error; err != nil {
		return endpoint.List[Contact]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list contacts")
	}

	return endpoint.List[Contact]{Data: contactsOf(contacts), Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total}}, nil
}

// createContactRequest is a new contact: an address, and optionally a name and how far it is
// trusted (trusted when left out). The address and the trust level are checked in the
// handler, together.
type createContactRequest struct {
	ID   uint32            `path:"id" minimum:"1" example:"1"`
	Body createContactBody `body:""`
}

type createContactBody struct {
	Email      string  `json:"email"`
	Name       string  `json:"name,omitempty"`
	TrustLevel *string `json:"trust_level,omitempty"`
}

// create adds a new contact to a mailbox.
// POST /api/v1/accounts/{id}/contacts
func (h *ContactHandler) create(r *http.Request, in createContactRequest) (Contact, error) {
	mailboxID, err := h.resolveMailboxID(r, in.ID, models.CapMailWrite)
	if err != nil {
		return Contact{}, err
	}

	req := in.Body
	errors := map[string]string{}
	if req.Email == "" {
		errors["email"] = "required"
	}
	if address, err := mail.ParseAddress(req.Email); req.Email != "" && (err != nil || address.Address != req.Email) {
		errors["email"] = "must be an email address"
	}
	trustLevel := "trusted"
	if req.TrustLevel != nil {
		trustLevel = *req.TrustLevel
		switch trustLevel {
		case "auto", "trusted", "blocked":
		default:
			errors["trust_level"] = "must be auto, trusted, or blocked"
		}
	}
	if len(errors) > 0 {
		return Contact{}, endpoint.Invalid(errors)
	}

	contact := models.Contact{
		MailboxID:  mailboxID,
		Email:      req.Email,
		Name:       req.Name,
		TrustLevel: trustLevel,
		Source:     "manual",
	}

	if err := h.db.WithContext(r.Context()).Create(&contact).Error; err != nil {
		if repositories.IsUniqueViolation(err) {
			return Contact{}, endpoint.Refuse(http.StatusConflict, "already_exists", "Contact already exists")
		}
		return Contact{}, fmt.Errorf("create contact: %w", err)
	}

	return contactOf(contact), nil
}

// updateContactRequest changes what is given; null or left out leaves a field as it is.
type updateContactRequest struct {
	ID   uint32            `path:"id" minimum:"1" example:"1"`
	CID  uint32            `path:"cid" minimum:"1" example:"1"`
	Body updateContactBody `body:""`
}

type updateContactBody struct {
	Name       *string `json:"name,omitempty"`
	TrustLevel *string `json:"trust_level,omitempty" enums:"auto,trusted,blocked" message:"must be auto, trusted, or blocked"`
}

// update updates an existing contact's fields.
// PATCH /api/v1/accounts/{id}/contacts/{cid}
func (h *ContactHandler) update(r *http.Request, in updateContactRequest) (Contact, error) {
	mailboxID, err := h.resolveMailboxID(r, in.ID, models.CapMailWrite)
	if err != nil {
		return Contact{}, err
	}

	var contact models.Contact
	if err := h.db.WithContext(r.Context()).Where("id = ? AND mailbox_id = ?", in.CID, mailboxID).First(&contact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Contact{}, errContactNotFound
		}
		return Contact{}, fmt.Errorf("find contact for update: %w", err)
	}

	req := in.Body
	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.TrustLevel != nil {
		updates["trust_level"] = *req.TrustLevel
	}

	if len(updates) > 0 {
		if err := h.db.WithContext(r.Context()).Model(&contact).Updates(updates).Error; err != nil {
			return Contact{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to update contact")
		}
	}

	if err := h.db.WithContext(r.Context()).Where("id = ? AND mailbox_id = ?", in.CID, mailboxID).First(&contact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Contact{}, errContactNotFound
		}
		return Contact{}, fmt.Errorf("reload updated contact: %w", err)
	}
	return contactOf(contact), nil
}

// delete removes a contact from a mailbox.
// DELETE /api/v1/accounts/{id}/contacts/{cid}
func (h *ContactHandler) delete(r *http.Request, in contactPath) (endpoint.Empty, error) {
	mailboxID, err := h.resolveMailboxID(r, in.ID, models.CapMailWrite)
	if err != nil {
		return endpoint.Empty{}, err
	}

	var contact models.Contact
	if err := h.db.Where("id = ? AND mailbox_id = ?", in.CID, mailboxID).First(&contact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return endpoint.Empty{}, errContactNotFound
		}
		return endpoint.Empty{}, fmt.Errorf("find contact for deletion: %w", err)
	}

	if err := h.db.Delete(&contact).Error; err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to delete contact")
	}

	return endpoint.Empty{}, nil
}

type blockSenderRequest struct {
	ID   uint32          `path:"id" minimum:"1" example:"1"`
	Body blockSenderBody `body:""`
}

type blockSenderBody struct {
	Email string `json:"email"`
}

// block creates or updates a contact with trust_level="blocked".
// POST /api/v1/accounts/{id}/contacts/block
func (h *ContactHandler) block(r *http.Request, in blockSenderRequest) (endpoint.Status[Contact], error) {
	mailboxID, err := h.resolveMailboxID(r, in.ID, models.CapMailWrite)
	if err != nil {
		return endpoint.Status[Contact]{}, err
	}

	email := in.Body.Email
	if email == "" {
		return endpoint.Status[Contact]{}, endpoint.Invalid(map[string]string{"email": "required"})
	}

	var contact models.Contact
	result := h.db.WithContext(r.Context()).Where("mailbox_id = ? AND email = ?", mailboxID, email).First(&contact)

	if result.Error == nil {
		// Contact exists, update to blocked
		if err := h.db.WithContext(r.Context()).Model(&contact).Updates(map[string]interface{}{
			"trust_level": "blocked",
			"source":      "manual",
		}).Error; err != nil {
			return endpoint.Status[Contact]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to block sender")
		}
		if err := h.db.WithContext(r.Context()).Where("id = ? AND mailbox_id = ?", contact.ID, mailboxID).First(&contact).Error; err != nil {
			return endpoint.Status[Contact]{}, fmt.Errorf("reload blocked contact: %w", err)
		}
		return endpoint.Status[Contact]{Code: http.StatusOK, Value: contactOf(contact)}, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return endpoint.Status[Contact]{}, fmt.Errorf("find contact to block: %w", result.Error)
	}

	// Contact does not exist, create as blocked
	contact = models.Contact{
		MailboxID:  mailboxID,
		Email:      email,
		TrustLevel: "blocked",
		Source:     "manual",
	}

	if err := h.db.WithContext(r.Context()).Create(&contact).Error; err != nil {
		return endpoint.Status[Contact]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to block sender")
	}

	return endpoint.Status[Contact]{Code: http.StatusCreated, Value: contactOf(contact)}, nil
}

type suggestContactsRequest struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
	Q  string `query:"q"`
}

// suggest returns autocomplete suggestions for the compose "To" field.
// GET /api/v1/accounts/{id}/contacts/suggest?q=...
func (h *ContactHandler) suggest(r *http.Request, in suggestContactsRequest) (endpoint.List[Contact], error) {
	mailboxID, err := h.resolveMailboxID(r, in.ID, models.CapMailRead)
	if err != nil {
		return endpoint.List[Contact]{}, err
	}

	if in.Q == "" {
		return endpoint.List[Contact]{Data: []Contact{}}, nil
	}

	var contacts []models.Contact
	escaped := "%" + escapeLike(in.Q) + "%"
	if err := h.db.WithContext(r.Context()).Where("mailbox_id = ? AND (email LIKE ? ESCAPE '\\' OR name LIKE ? ESCAPE '\\') AND trust_level != ?",
		mailboxID, escaped, escaped, "blocked").
		Order("name ASC, email ASC").
		Limit(10).
		Find(&contacts).Error; err != nil {
		return endpoint.List[Contact]{}, fmt.Errorf("suggest contacts: %w", err)
	}

	return endpoint.List[Contact]{Data: contactsOf(contacts)}, nil
}

type importContactsRequest struct {
	ID   uint32             `path:"id" minimum:"1" example:"1"`
	Body importContactsBody `body:""`
}

type importContactsBody struct {
	Contacts []importContactEntry `json:"contacts"`
}

type importContactEntry struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// importContacts bulk-imports contacts for a mailbox.
// POST /api/v1/accounts/{id}/contacts/import
func (h *ContactHandler) importContacts(r *http.Request, in importContactsRequest) (ContactsImported, error) {
	mailboxID, err := h.resolveMailboxID(r, in.ID, models.CapMailWrite)
	if err != nil {
		return ContactsImported{}, err
	}

	entries := in.Body.Contacts
	if len(entries) == 0 {
		return ContactsImported{}, endpoint.Invalid(map[string]string{"contacts": "required"})
	}

	// Validate the whole batch before writing so a bad entry cannot leave a
	// partially imported request behind.
	errors := map[string]string{}
	for i, entry := range entries {
		field := fmt.Sprintf("contacts[%d].email", i)
		if entry.Email == "" {
			errors[field] = "required"
		} else if address, err := mail.ParseAddress(entry.Email); err != nil || address.Address != entry.Email {
			errors[field] = "must be an email address"
		}
	}
	if len(errors) > 0 {
		return ContactsImported{}, endpoint.Invalid(errors)
	}

	imported := 0
	if err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		for _, entry := range entries {
			contact := models.Contact{
				MailboxID:  mailboxID,
				Email:      entry.Email,
				Name:       entry.Name,
				TrustLevel: "auto",
				Source:     "import",
			}
			// A duplicate must not abort PostgreSQL's batch transaction. Other
			// failures roll back the whole batch rather than reporting a partial
			// import as a success.
			result := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "mailbox_id"}, {Name: "email"}},
				DoNothing: true,
			}).Create(&contact)
			if result.Error != nil {
				return fmt.Errorf("import contact: %w", result.Error)
			}
			imported += int(result.RowsAffected)
		}
		return nil
	}); err != nil {
		return ContactsImported{}, err
	}

	return ContactsImported{Imported: imported}, nil
}
