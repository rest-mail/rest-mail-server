package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
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
	ID                uint      `json:"id"`
	MailboxID         uint      `json:"mailbox_id"`
	Email             string    `json:"email"`
	Name              string    `json:"name"`
	TrustLevel        string    `json:"trust_level" enums:"auto,trusted,blocked"`
	Source            string    `json:"source" enums:"sent,manual,import"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	UsualReplySeconds *int64    `json:"usual_reply_seconds,omitempty" minimum:"1" description:"Median seconds to this contact's first reply to each sent message, from this account's history only. Omitted when disabled or fewer than three matched replies exist."`
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
	// Skipped is how many entries named a contact the account already has.
	Skipped int `json:"skipped"`
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
	h.registerQuietHours(rs)
	h.registerAccountTimeZone(rs)
	schemes := []string{"CookieAuth", "BearerAuth", "APIKeyAuth", "BasicAuth"}
	reads := handlerChecked(models.CapMailRead, schemes...)
	writes := handlerChecked(models.CapMailWrite, schemes...)
	tags := []string{"Contacts"}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/contact-reply-time",
		OperationID: "getContactReplyTimeSetting", Summary: "Get the account's contact reply-time setting", Tags: tags,
		Description: "Off by default. Only this account's stored mail is used; no history is calculated or returned while disabled. Nothing is sent to contacts or external services.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: reads}, h.getReplyTimeSetting)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/accounts/{id}/contact-reply-time",
		OperationID: "setContactReplyTimeSetting", Summary: "Set the account's contact reply-time setting", Tags: tags,
		Description: "Opt in to contact reply-time estimates in contact lists and suggestions. Uses the median of at least three first replies matched by In-Reply-To, or the last References ID when In-Reply-To is absent, to this account's Sent messages addressed to the contact. Uses stored received_at timestamps, ignores drafts, deleted mail and nonpositive delays, and counts each sent Message-ID once. This is a local estimate, not a signal from the contact. No headers, outbound requests or tracking are added.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: writes}, h.setReplyTimeSetting)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/contacts",
		OperationID: "listContacts", Summary: "List contacts for an account", Tags: tags,
		Errors: []int{http.StatusForbidden, http.StatusNotFound}, Security: reads}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/contacts",
		OperationID: "createContact", Summary: "Create a new contact", Tags: tags, Status: http.StatusCreated,
		Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict}, Security: writes}, h.create)
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
		Errors: []int{http.StatusForbidden, http.StatusNotFound}, Security: writes}, h.block)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/contacts/import",
		OperationID: "importContacts", Summary: "Bulk import contacts", Tags: tags, DescribedRequest: importContactsRequest{},
		Description: "As many contacts as fit the server's contact import limit, 10 MiB unless an administrator changes it. Every entry is checked first, and contacts the account already has are skipped.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: writes}, h.importContacts)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/contacts/suggest",
		OperationID: "suggestContacts", Summary: "Autocomplete contacts by query", Tags: tags,
		Description: "At most 10 contacts whose address or name contains q, blocked ones left out; an empty q answers none.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound}, Security: reads}, h.suggest)
}

var (
	errContactAccess   = errAccountNotFound
	errContactNotFound = endpoint.Refuse(http.StatusNotFound, "not_found", "Contact not found")
)

// resolveMailboxID checks the caller may act on the account (mailbox) the
// URL names with capability, and returns its id.
func (h *ContactHandler) resolveMailboxID(r *http.Request, accountID uint32, capability string) (uint, error) {
	mailbox, err := h.resolveContactAccount(r, accountID, capability)
	if err != nil {
		return 0, err
	}
	return mailbox.ID, nil
}

func (h *ContactHandler) resolveContactAccount(r *http.Request, accountID uint32, capability string) (*models.Mailbox, error) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		return nil, errContactAccess
	}
	mailbox, err := mayReach(h.db.WithContext(r.Context()), claims, uint(accountID), capability)
	if err != nil {
		if errors.Is(err, errNoAccess) {
			return nil, errContactAccess
		}
		return nil, fmt.Errorf("resolve contacts account: %w", err)
	}
	return mailbox, nil
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
	mailbox, err := h.resolveContactAccount(r, in.ID, models.CapMailRead)
	if err != nil {
		return endpoint.List[Contact]{}, err
	}

	query := h.db.WithContext(r.Context()).Model(&models.Contact{}).Where("mailbox_id = ?", mailbox.ID)

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
	if err := query.Count(&total).Error; err != nil {
		return endpoint.List[Contact]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list contacts")
	}

	var contacts []models.Contact
	if err := query.Order("email ASC").Limit(limit).Offset(offset).Find(&contacts).Error; err != nil {
		return endpoint.List[Contact]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list contacts")
	}

	out, err := h.contactsWithReplyTime(r, mailbox, contacts)
	return endpoint.List[Contact]{Data: out, Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total}}, err
}

// createContactRequest is a new contact: an address, and optionally a name and how far it is
// trusted (trusted when left out). The address and the trust level are checked in the
// handler, together.
type createContactRequest struct {
	ID   uint32            `path:"id" minimum:"1" example:"1"`
	Body createContactBody `body:""`
}

type createContactBody struct {
	Email      string  `json:"email" format:"email" maxLength:"255" formatMessage:"must be an email address"`
	Name       string  `json:"name,omitempty" maxLength:"255"`
	TrustLevel *string `json:"trust_level,omitempty" enums:"auto,trusted,blocked" message:"must be auto, trusted, or blocked"`
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
	Name       *string `json:"name,omitempty" maxLength:"255"`
	TrustLevel *string `json:"trust_level,omitempty" enums:"auto,trusted,blocked" message:"must be auto, trusted, or blocked"`
}

// update updates an existing contact's fields.
// PATCH /api/v1/accounts/{id}/contacts/{cid}
func (h *ContactHandler) update(r *http.Request, in updateContactRequest) (Contact, error) {
	mailboxID, err := h.resolveMailboxID(r, in.ID, models.CapMailWrite)
	if err != nil {
		return Contact{}, err
	}

	req := in.Body
	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.TrustLevel != nil {
		updates["trust_level"] = *req.TrustLevel
	}

	var contact models.Contact
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND mailbox_id = ?", in.CID, mailboxID).First(&contact).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errContactNotFound
			}
			return fmt.Errorf("find contact for update: %w", err)
		}
		if len(updates) > 0 {
			result := tx.Model(&contact).Where("mailbox_id = ?", mailboxID).Updates(updates)
			if result.Error != nil {
				return endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to update contact")
			}
			if result.RowsAffected == 0 {
				return errContactNotFound
			}
		}
		if err := tx.Where("id = ? AND mailbox_id = ?", in.CID, mailboxID).First(&contact).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errContactNotFound
			}
			return fmt.Errorf("reload updated contact: %w", err)
		}
		return nil
	})
	if err != nil {
		return Contact{}, err
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

	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		var contact models.Contact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND mailbox_id = ?", in.CID, mailboxID).First(&contact).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errContactNotFound
			}
			return fmt.Errorf("find contact for deletion: %w", err)
		}
		result := tx.Where("id = ? AND mailbox_id = ?", in.CID, mailboxID).Delete(&models.Contact{})
		if result.Error != nil {
			return endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to delete contact")
		}
		if result.RowsAffected == 0 {
			return errContactNotFound
		}
		return nil
	})
	if err != nil {
		return endpoint.Empty{}, err
	}
	return endpoint.Empty{}, nil
}

type blockSenderRequest struct {
	ID   uint32          `path:"id" minimum:"1" example:"1"`
	Body blockSenderBody `body:""`
}

type blockSenderBody struct {
	Email string `json:"email" format:"email" maxLength:"255" formatMessage:"must be an email address"`
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
	status := http.StatusCreated
	err = h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("mailbox_id = ? AND email = ?", mailboxID, email).First(&contact)
		if result.Error == nil {
			result := tx.Model(&contact).Where("mailbox_id = ?", mailboxID).Updates(map[string]interface{}{
				"trust_level": "blocked",
				"source":      "manual",
			})
			if result.Error != nil {
				return endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to block sender")
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("contact disappeared while blocking sender")
			}
			if err := tx.Where("id = ? AND mailbox_id = ?", contact.ID, mailboxID).First(&contact).Error; err != nil {
				return fmt.Errorf("reload blocked contact: %w", err)
			}
			status = http.StatusOK
			return nil
		}
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return fmt.Errorf("find contact to block: %w", result.Error)
		}
		contact = models.Contact{
			MailboxID:  mailboxID,
			Email:      email,
			TrustLevel: "blocked",
			Source:     "manual",
		}
		if err := tx.Create(&contact).Error; err != nil {
			return endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to block sender")
		}
		return nil
	})
	if err != nil {
		return endpoint.Status[Contact]{}, err
	}
	return endpoint.Status[Contact]{Code: status, Value: contactOf(contact)}, nil
}

type suggestContactsRequest struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
	Q  string `query:"q"`
}

// suggest returns autocomplete suggestions for the compose "To" field.
// GET /api/v1/accounts/{id}/contacts/suggest?q=...
func (h *ContactHandler) suggest(r *http.Request, in suggestContactsRequest) (endpoint.List[Contact], error) {
	mailbox, err := h.resolveContactAccount(r, in.ID, models.CapMailRead)
	if err != nil {
		return endpoint.List[Contact]{}, err
	}

	if in.Q == "" {
		return endpoint.List[Contact]{Data: []Contact{}}, nil
	}

	var contacts []models.Contact
	escaped := "%" + escapeLike(in.Q) + "%"
	if err := h.db.WithContext(r.Context()).Where("mailbox_id = ? AND (email LIKE ? ESCAPE '\\' OR name LIKE ? ESCAPE '\\') AND trust_level != ?",
		mailbox.ID, escaped, escaped, "blocked").
		Order("name ASC, email ASC").
		Limit(10).
		Find(&contacts).Error; err != nil {
		return endpoint.List[Contact]{}, fmt.Errorf("suggest contacts: %w", err)
	}

	out, err := h.contactsWithReplyTime(r, mailbox, contacts)
	return endpoint.List[Contact]{Data: out}, err
}

// contactsPath names the account whose contacts a route acts on.
type contactsPath struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}

const (
	// importBatchSize is how many contacts one INSERT carries: a file's size is
	// limited by the request cap, its row count is not, and PostgreSQL limits a
	// statement's parameters.
	importBatchSize = 500
	// maxImportProblems is how many bad addresses one refusal names.
	maxImportProblems = 100
)

type importContactsRequest struct {
	ID   uint32             `path:"id" minimum:"1" example:"1"`
	Body importContactsBody `body:""`
}

type importContactsBody struct {
	Contacts []importContactEntry `json:"contacts" minItems:"1" message:"required" missing:"required"`
}

type importContactEntry struct {
	Email string `json:"email" format:"email" maxLength:"255" formatMessage:"must be an email address" missing:"required"`
	Name  string `json:"name,omitempty" maxLength:"255"`
}

var errContactImportTooLarge = endpoint.Refuse(http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")

// ContactImportRoute says a request is a contact import, which has a limit of its own that an
// administrator can set above the API's general one.
func ContactImportRoute(r *http.Request) bool {
	return r.Method == http.MethodPost && contactImportPath.MatchString(r.URL.Path)
}

var contactImportPath = regexp.MustCompile(`^/api/v1/accounts/[^/]+/contacts/import/?$`)

// importContacts bulk-imports contacts for a mailbox. The upload goes to a file first and
// is read an entry at a time, twice: a check of every entry, then the writes. Its size
// never sits in memory.
// POST /api/v1/accounts/{id}/contacts/import
func (h *ContactHandler) importContacts(r *http.Request, in contactsPath) (ContactsImported, error) {
	// This route is outside the API's general body limit (ContactImportRoute), so it is held
	// to its own here, before any of the upload is read. An upload within the default takes
	// no database read yet: a bad one is refused without one.
	allowed := defaultContactImportMaxBytes
	if r.ContentLength < 0 || r.ContentLength > allowed {
		// When a higher limit can't be confirmed, the upload is held to the default.
		if stored, err := contactImportMaxBytes(h.db.WithContext(r.Context())); err != nil {
			slog.Error("could not read the contact import limit", "error", err)
		} else {
			allowed = stored
		}
	}
	if r.ContentLength > allowed {
		return ContactsImported{}, errContactImportTooLarge
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(nil, r.Body, allowed)
	}
	file, done, err := endpoint.SpoolBody(r, "application/json")
	if err != nil {
		return ContactsImported{}, err
	}
	defer done()

	// Validate the whole file before writing so a bad entry cannot leave a
	// partially imported request behind.
	problems := map[string]string{}
	total, err := eachImportEntry(file, func(_ int, _ importContactEntry, found map[string]string) error {
		for field, problem := range found {
			if len(problems) < maxImportProblems {
				problems[field] = problem
			}
		}
		return nil
	})
	if err != nil {
		return ContactsImported{}, err
	}
	if len(problems) > 0 {
		return ContactsImported{}, endpoint.Invalid(problems)
	}
	if total == 0 {
		return ContactsImported{}, endpoint.Invalid(map[string]string{"contacts": "required"})
	}

	mailboxID, err := h.resolveMailboxID(r, in.ID, models.CapMailWrite)
	if err != nil {
		return ContactsImported{}, err
	}
	// An administrator may have set the limit below the default.
	limit, err := contactImportMaxBytes(h.db.WithContext(r.Context()))
	if err != nil {
		return ContactsImported{}, err
	}
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return ContactsImported{}, fmt.Errorf("measure the contact import: %w", err)
	}
	if size > limit {
		return ContactsImported{}, errContactImportTooLarge
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ContactsImported{}, fmt.Errorf("rewind the contact import: %w", err)
	}

	imported := 0
	if err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		// A duplicate must not abort PostgreSQL's batch transaction. Other
		// failures roll back the whole import rather than reporting a partial
		// import as a success.
		batch := make([]models.Contact, 0, importBatchSize)
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			result := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "mailbox_id"}, {Name: "email"}},
				DoNothing: true,
			}).Create(&batch)
			if result.Error != nil {
				return fmt.Errorf("import contact: %w", result.Error)
			}
			imported += int(result.RowsAffected)
			batch = batch[:0]
			return nil
		}
		if _, err := eachImportEntry(file, func(_ int, entry importContactEntry, _ map[string]string) error {
			batch = append(batch, models.Contact{MailboxID: mailboxID, Email: entry.Email, Name: entry.Name, TrustLevel: "auto", Source: "import"})
			if len(batch) == importBatchSize {
				return flush()
			}
			return nil
		}); err != nil {
			return err
		}
		return flush()
	}); err != nil {
		return ContactsImported{}, err
	}

	return ContactsImported{Imported: imported, Skipped: total - imported}, nil
}

// eachImportEntry walks a {"contacts":[...]} file one entry at a time, holding one entry
// in memory. problems is what the type's rules and the address check say about the entry,
// by field; a body that isn't that shape is the refusal a bound body would get. It answers
// how many entries there were.
func eachImportEntry(file io.Reader, each func(int, importContactEntry, map[string]string) error) (int, error) {
	unreadable := endpoint.Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
	decoder := json.NewDecoder(file)
	first, err := decoder.Token()
	if err != nil {
		return 0, unreadable
	}
	if first != json.Delim('{') {
		return 0, endpoint.Invalid(map[string]string{"body": "the body is not a JSON object"})
	}
	total := 0
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return 0, unreadable
		}
		if key != "contacts" {
			// The request type refuses a field it doesn't declare; ask it, for its words.
			name, _ := json.Marshal(key)
			return 0, endpoint.BindBody(&importContactsBody{}, []byte(`{"contacts":[{"email":"a@example.org"}],`+string(name)+`:null}`))
		}
		open, err := decoder.Token()
		if err != nil {
			return 0, unreadable
		}
		if open == nil {
			continue
		}
		if open != json.Delim('[') {
			return 0, endpoint.Invalid(map[string]string{"contacts": "required"})
		}
		for decoder.More() {
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return 0, unreadable
			}
			entry, problems := readImportEntry(raw, total)
			if err := each(total, entry, problems); err != nil {
				return 0, err
			}
			total++
		}
		if _, err := decoder.Token(); err != nil {
			return 0, unreadable
		}
	}
	if _, err := decoder.Token(); err != nil {
		return 0, unreadable
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return 0, unreadable
	}
	return total, nil
}

// readImportEntry holds one entry to the rules the request type states, by the same binder
// a bound body goes through, and then to the address check.
func readImportEntry(raw json.RawMessage, index int) (importContactEntry, map[string]string) {
	var entry importContactEntry
	prefix := fmt.Sprintf("contacts[%d]", index)
	var refusal *endpoint.Error
	if err := endpoint.BindBody(&entry, raw); errors.As(err, &refusal) {
		problems := map[string]string{}
		for field, problem := range refusal.Fields {
			if field == "body" {
				problems[prefix] = problem
				continue
			}
			problems[prefix+"."+field] = problem
		}
		if len(problems) == 0 {
			problems[prefix] = refusal.Message
		}
		return entry, problems
	} else if err != nil {
		return entry, map[string]string{prefix: "could not be read"}
	}
	if address, err := mail.ParseAddress(entry.Email); err != nil || address.Address != entry.Email {
		return entry, map[string]string{prefix + ".email": "must be an email address"}
	}
	return entry, nil
}
