package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	apitypes "github.com/restmail/restmail/internal/api/types"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

// Mailbox is the admin account response. Credentials and session secrets have no API field.
type Mailbox = apitypes.Mailbox

func mailboxOf(m models.Mailbox) Mailbox {
	var domain *Domain
	if m.Domain.ID != 0 {
		loaded := domainOf(m.Domain)
		domain = &loaded
	}
	return Mailbox{
		ID:                     m.ID,
		DomainID:               m.DomainID,
		LocalPart:              m.LocalPart,
		Address:                m.Address,
		DisplayName:            m.DisplayName,
		QuotaBytes:             m.QuotaBytes,
		QuotaUsedBytes:         m.QuotaUsedBytes,
		Active:                 m.Active,
		LastLoginAt:            m.LastLoginAt,
		PasswordChangeRequired: m.PasswordChangeRequired,
		TwoFactorRequired:      m.TwoFactorRequired,
		LastPasswordChange:     m.LastPasswordChange,
		CreatedAt:              m.CreatedAt,
		UpdatedAt:              m.UpdatedAt,
		Domain:                 domain,
		QuotaUsage:             quotaUsageOf(m.QuotaUsage),
	}
}

type Alias struct {
	ID                 uint      `json:"id"`
	DomainID           uint      `json:"domain_id"`
	SourceAddress      string    `json:"source_address" format:"idn-email"`
	DestinationAddress string    `json:"destination_address" description:"Remote address retaining its submitted display name, quoting and internationalized spelling"`
	Active             bool      `json:"active"`
	CreatedAt          time.Time `json:"created_at"`
	Domain             *Domain   `json:"domain,omitempty"`
}

func aliasOf(m models.Alias) Alias {
	var domain *Domain
	if m.Domain.ID != 0 {
		loaded := domainOf(m.Domain)
		domain = &loaded
	}
	return Alias{
		ID:                 m.ID,
		DomainID:           m.DomainID,
		SourceAddress:      m.SourceAddress,
		DestinationAddress: m.DestinationAddress,
		Active:             m.Active,
		CreatedAt:          m.CreatedAt,
		Domain:             domain,
	}
}

type QuotaUsage = apitypes.QuotaUsage

func quotaUsageOf(m *models.QuotaUsage) *QuotaUsage {
	if m == nil {
		return nil
	}
	var mailbox *Mailbox
	if m.Mailbox.ID != 0 {
		loaded := mailboxOf(m.Mailbox)
		mailbox = &loaded
	}
	return &QuotaUsage{
		MailboxID:       m.MailboxID,
		SubjectBytes:    m.SubjectBytes,
		BodyBytes:       m.BodyBytes,
		AttachmentBytes: m.AttachmentBytes,
		MessageCount:    m.MessageCount,
		UpdatedAt:       m.UpdatedAt,
		Mailbox:         mailbox,
	}
}

// Parameters are bound before the typed handler runs; pagination keeps its existing defaults.
type adminAddressListRequest struct {
	DomainID *int `query:"domain_id"`
	Limit    *int `query:"limit"`
	Offset   *int `query:"offset"`
}
type adminAddressPath struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}
type updateMailboxInput struct {
	ID   uint32               `path:"id" minimum:"1" example:"1"`
	Body updateMailboxRequest `body:""`
}
type updateAliasInput struct {
	ID   uint32             `path:"id" minimum:"1" example:"1"`
	Body updateAliasRequest `body:""`
}

// MailboxRouteChecks bind resource checks to their descriptions.
type MailboxRouteChecks struct {
	OnList    func(string) endpoint.Middleware
	OnMailbox func(string) endpoint.Middleware
}

// AliasRouteChecks use the alias's source domain, never its destination mailbox.
type AliasRouteChecks struct {
	OnList  func(string) endpoint.Middleware
	OnAlias func(string) endpoint.Middleware
}

// Register mounts the five admin mailboxes operations with their request and response types.
func (h *MailboxHandler) Register(rs endpoint.Routes, checks MailboxRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/mailboxes",
		OperationID: "listMailboxes", Summary: "List mailboxes, optionally filtered by domain", Tags: []string{"Admin: Mailboxes"}, Status: http.StatusOK,
		Middleware: []endpoint.Middleware{checks.OnList(middleware.CapMailboxesRead)}}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/mailboxes/{id}",
		OperationID: "getMailbox", Summary: "Get an account by ID", Tags: []string{"Admin: Mailboxes"}, Status: http.StatusOK,
		Errors: []int{http.StatusNotFound}, Middleware: []endpoint.Middleware{checks.OnMailbox(middleware.CapMailboxesRead)}}, h.get)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPatch, Path: "/api/v1/admin/mailboxes/{id}",
		OperationID: "updateMailbox", Summary: "Update an account", Tags: []string{"Admin: Mailboxes"}, Status: http.StatusOK,
		Errors: []int{http.StatusNotFound}, Middleware: []endpoint.Middleware{checks.OnMailbox(middleware.CapMailboxesWrite)}}, h.update)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/mailboxes/{id}",
		OperationID: "deleteMailbox", Summary: "Delete an account", Tags: []string{"Admin: Mailboxes"}, Status: http.StatusNoContent,
		Errors: []int{http.StatusNotFound, http.StatusConflict, http.StatusForbidden}, Middleware: []endpoint.Middleware{checks.OnMailbox(middleware.CapMailboxesDel)}}, h.delete)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/mailboxes/{id}/sessions",
		OperationID: "endMailboxSessions", Summary: "Sign an account out everywhere", Tags: []string{"Admin: Mailboxes"}, Status: http.StatusNoContent,
		Errors: []int{http.StatusNotFound, http.StatusTooManyRequests}, Middleware: []endpoint.Middleware{checks.OnMailbox(middleware.CapMailboxesWrite)}}, h.endSessions)
}

// Register mounts the five admin aliases operations with their request and response types.
func (h *AliasHandler) Register(rs endpoint.Routes, checks AliasRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/aliases",
		OperationID: "listAliases", Summary: "List aliases, optionally filtered by domain", Tags: []string{"Admin: Aliases"}, Status: http.StatusOK,
		Middleware: []endpoint.Middleware{checks.OnList(middleware.CapMailboxesRead)}}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/aliases/{id}",
		OperationID: "getAlias", Summary: "Get an alias by ID", Tags: []string{"Admin: Aliases"}, Status: http.StatusOK,
		Errors: []int{http.StatusNotFound}, Middleware: []endpoint.Middleware{checks.OnAlias(middleware.CapMailboxesRead)}}, h.get)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPatch, Path: "/api/v1/admin/aliases/{id}",
		OperationID: "updateAlias", Summary: "Update an alias", Tags: []string{"Admin: Aliases"}, Status: http.StatusOK,
		Errors: []int{http.StatusNotFound, http.StatusTooManyRequests}, Middleware: []endpoint.Middleware{checks.OnAlias(middleware.CapMailboxesWrite)}}, h.update)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/aliases/{id}",
		OperationID: "deleteAlias", Summary: "Delete an alias", Tags: []string{"Admin: Aliases"}, Status: http.StatusNoContent,
		Errors: []int{http.StatusNotFound}, Middleware: []endpoint.Middleware{checks.OnAlias(middleware.CapMailboxesDel)}}, h.delete)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/aliases",
		OperationID: "createAlias", Summary: "Create a new alias", Tags: []string{"Admin: Aliases"}, Status: http.StatusCreated,
		Errors: []int{http.StatusNotFound, http.StatusConflict, http.StatusForbidden, http.StatusTooManyRequests}, Middleware: []endpoint.Middleware{checks.OnList(middleware.CapMailboxesWrite)}}, h.create)
}

// mailboxGrantError preserves the grant API's reachable deletion refusals.
func mailboxGrantError(err error) error {
	switch {
	case errors.Is(err, errTwoFactorRequired):
		return endpoint.Refuse(http.StatusForbidden, "two_factor_required", "Changing grants needs a session signed in with two-factor authentication: sign in again with your code")
	case errors.Is(err, errNoSuchGrant):
		return endpoint.Refuse(http.StatusNotFound, "not_found", "Grant not found")
	case errors.Is(err, grants.ErrExceedsHeld):
		return endpoint.Refuse(http.StatusForbidden, "grant_exceeds_held", err.Error())
	case errors.Is(err, grants.ErrExists):
		return endpoint.Refuse(http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, grants.ErrLastSuperadmin):
		return endpoint.Refuse(http.StatusConflict, "last_superadmin", "Refused: "+err.Error()+"; nobody could grant anything after it")
	default:
		return endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to change grants")
	}
}
