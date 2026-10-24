package handlers

import (
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
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

// SieveScript is the account's Sieve script. With none stored it is the empty script, not
// active, with no id, hash or timestamps; a stored one has them all, its hash being the
// sha256 hex of the script validated at install.
type SieveScript struct {
	ID          uint       `json:"id,omitempty"`
	MailboxID   uint       `json:"mailbox_id"`
	Script      string     `json:"script"`
	ScriptHash  *string    `json:"script_hash,omitempty"`
	ValidatedAt *time.Time `json:"validated_at,omitempty"`
	Active      bool       `json:"active"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

func sieveScriptOf(m models.SieveScript) SieveScript {
	return SieveScript{
		ID:          m.ID,
		MailboxID:   m.MailboxID,
		Script:      m.Script,
		ScriptHash:  &m.ScriptHash,
		ValidatedAt: m.ValidatedAt,
		Active:      m.Active,
		CreatedAt:   &m.CreatedAt,
		UpdatedAt:   &m.UpdatedAt,
	}
}

// SieveDeleted is the answer to deleting the script, whether or not there was one.
type SieveDeleted struct {
	Status string `json:"status" enums:"deleted"`
}

// SieveValidation says whether a script would be accepted, and why not: the parser's message,
// with its line and column, or the dry run's.
type SieveValidation struct {
	Message string `json:"message"`
	Valid   bool   `json:"valid"`
}

// putSieveRequest replaces the account's script. script is required (null is refused like a
// missing one); active defaults to true.
type putSieveRequest struct {
	ID   uint32       `path:"id" minimum:"1"`
	Body putSieveBody `body:""`
}

type putSieveBody struct {
	Script *string `json:"script"`
	Active *bool   `json:"active,omitempty"`
}

type validateSieveRequest struct {
	ID   uint32            `path:"id" minimum:"1"`
	Body validateSieveBody `body:""`
}

type validateSieveBody struct {
	Script *string `json:"script"`
}

var errSieveScriptRequired = endpoint.Invalid(map[string]string{"script": "required"})

// Register mounts the account's Sieve script.
func (h *SieveHandler) Register(rs endpoint.Routes) {
	tags := []string{"Sieve"}
	reads := handlerChecked(models.CapMailRead, mailRulesSchemes...)
	writes := handlerChecked(models.CapMailWrite, mailRulesSchemes...)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/accounts/{id}/sieve",
		OperationID: "getSieveScript", Summary: "Get the Sieve filter script", Tags: tags,
		Errors: []int{http.StatusForbidden}, Security: reads}, h.get)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/accounts/{id}/sieve",
		OperationID: "putSieveScript", Summary: "Create or replace the Sieve filter script", Tags: tags,
		Description: "201 when it creates the script, 200 when it replaces it. A script that does not parse, or " +
			"fails its dry run, is 400 invalid_sieve with the parser's message, and is not stored.",
		Status: http.StatusOK, SuccessStatuses: []int{http.StatusCreated},
		Errors: []int{http.StatusForbidden}, Security: writes}, h.put)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/accounts/{id}/sieve",
		OperationID: "deleteSieveScript", Summary: "Delete the Sieve filter script", Tags: tags,
		Errors: []int{http.StatusForbidden}, Security: writes}, h.delete)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/accounts/{id}/sieve/validate",
		OperationID: "validateSieveScript", Summary: "Validate a Sieve script without saving", Tags: tags,
		Errors: []int{http.StatusForbidden}, Security: writes}, h.validate)
}

// get returns the Sieve script for a mailbox, or the empty script when it has none.
// GET /api/v1/accounts/{id}/sieve
func (h *SieveHandler) get(r *http.Request, in accountPath) (SieveScript, error) {
	mailboxID, err := mailRulesMailbox(h.db, r, in.ID)
	if err != nil {
		return SieveScript{}, err
	}

	var script models.SieveScript
	if err := h.db.Where("mailbox_id = ?", mailboxID).First(&script).Error; err != nil {
		return SieveScript{MailboxID: mailboxID}, nil
	}
	return sieveScriptOf(script), nil
}

// put creates or updates the Sieve script for a mailbox.
// PUT /api/v1/accounts/{id}/sieve
func (h *SieveHandler) put(r *http.Request, in putSieveRequest) (endpoint.Status[SieveScript], error) {
	mailboxID, err := mailRulesMailbox(h.db, r, in.ID)
	if err != nil {
		return endpoint.Status[SieveScript]{}, err
	}

	req := in.Body
	if req.Script == nil {
		return endpoint.Status[SieveScript]{}, errSieveScriptRequired
	}

	// Validate at INSTALL time: parse + a safe, side-effect-free dry-run. A script
	// that fails here is rejected NOW with HTTP 400 and an actionable message
	// (parse error + the extension missing a `require`, or the dry-run failure) and
	// is NOT stored — moving the failure off the delivery path, where a bad script
	// fails closed and silently defers the mailbox's mail.
	if err := filters.ValidateSieveForInstall(*req.Script); err != nil {
		return endpoint.Status[SieveScript]{}, endpoint.Refuse(http.StatusBadRequest, "invalid_sieve", err.Error())
	}

	active := true
	if req.Active != nil {
		active = *req.Active
	}

	// Record the "validated at install" marker: the sha256 hex of the exact stored
	// bytes and the validation time. The delivery path re-verifies this hash before
	// trusting the script (see filters.newSieveFilter).
	now := time.Now()
	scriptHash := filters.HashSieveScript(*req.Script)

	// Upsert
	var script models.SieveScript
	result := h.db.Where("mailbox_id = ?", mailboxID).First(&script)
	if result.Error != nil {
		// Create new
		script = models.SieveScript{
			MailboxID:   mailboxID,
			Script:      *req.Script,
			ScriptHash:  scriptHash,
			ValidatedAt: &now,
			Active:      active,
		}
		if err := h.db.Create(&script).Error; err != nil {
			return endpoint.Status[SieveScript]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to create script")
		}
		return endpoint.Status[SieveScript]{Code: http.StatusCreated, Value: sieveScriptOf(script)}, nil
	}

	// Update existing
	script.Script = *req.Script
	script.ScriptHash = scriptHash
	script.ValidatedAt = &now
	script.Active = active
	if err := h.db.Save(&script).Error; err != nil {
		return endpoint.Status[SieveScript]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to update script")
	}

	return endpoint.Status[SieveScript]{Code: http.StatusOK, Value: sieveScriptOf(script)}, nil
}

// delete removes the Sieve script for a mailbox.
// DELETE /api/v1/accounts/{id}/sieve
func (h *SieveHandler) delete(r *http.Request, in accountPath) (SieveDeleted, error) {
	mailboxID, err := mailRulesMailbox(h.db, r, in.ID)
	if err != nil {
		return SieveDeleted{}, err
	}

	h.db.Where("mailbox_id = ?", mailboxID).Delete(&models.SieveScript{})
	return SieveDeleted{Status: "deleted"}, nil
}

// validate checks a Sieve script without saving it.
// POST /api/v1/accounts/{id}/sieve/validate
func (h *SieveHandler) validate(r *http.Request, in validateSieveRequest) (SieveValidation, error) {
	// The same check as put (mail:write on the account): validating is part of
	// changing the account's script, and nobody else may run it there.
	if _, err := mailRulesMailbox(h.db, r, in.ID); err != nil {
		return SieveValidation{}, err
	}

	script := in.Body.Script
	if script == nil {
		return SieveValidation{}, errSieveScriptRequired
	}

	if *script == "" {
		return SieveValidation{Message: "Empty script is valid", Valid: true}, nil
	}

	// Same gate as install (put): parse + a safe dry-run, so "validate"
	// reports exactly what "save" would accept.
	if err := filters.ValidateSieveForInstall(*script); err != nil {
		return SieveValidation{Message: err.Error(), Valid: false}, nil
	}

	return SieveValidation{Message: "Script is valid", Valid: true}, nil
}
