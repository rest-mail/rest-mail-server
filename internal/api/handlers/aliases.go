package handlers

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
)

type AliasHandler struct {
	db *gorm.DB
}

func NewAliasHandler(db *gorm.DB) *AliasHandler {
	return &AliasHandler{db: db}
}

func (h *AliasHandler) list(r *http.Request, in adminAddressListRequest) (endpoint.List[Alias], error) {
	limit, offset := pagination(in.Limit, in.Offset, defaultListLimit, maxListLimit)

	query := filterAdminList(h.db.Model(&models.Alias{}), r, middleware.CapMailboxesRead, "aliases")
	if in.DomainID != nil {
		query = query.Where("domain_id = ?", *in.DomainID)
	}

	var total int64
	query.Count(&total)

	var aliases []models.Alias
	if err := query.Preload("Domain").Order("source_address ASC").Limit(limit).Offset(offset).Find(&aliases).Error; err != nil {
		return endpoint.List[Alias]{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to list aliases")
	}
	for i := range aliases {
		redactAdminDomain(r, &aliases[i].Domain)
	}
	out := make([]Alias, len(aliases))
	for i, row := range aliases {
		out[i] = aliasOf(row)
	}
	return endpoint.List[Alias]{Data: out, Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total}}, nil
}

// Address validation stays with the shared EAI parser: binding format:email
// would reject display-name addresses that existing clients can submit.
type createAliasRequest struct {
	SourceAddress      string `json:"source_address"`
	DestinationAddress string `json:"destination_address"`
}

func (h *AliasHandler) create(r *http.Request, in createAliasRequest) (Alias, error) {
	req := in

	errors := map[string]string{}
	if req.SourceAddress == "" {
		errors["source_address"] = "required"
	}
	if req.DestinationAddress == "" {
		errors["destination_address"] = "required"
	} else if rmail.ValidateAddress(req.DestinationAddress) != nil {
		errors["destination_address"] = "must be a valid email address"
	}
	if len(errors) > 0 {
		return Alias{}, endpoint.Invalid(errors)
	}

	// The source must be one mail address, as the spec's format: email says,
	// before its domain is looked up (#388): an "@" alone let "@domain",
	// "local@", two addresses and CR/LF-injected values through.
	if rmail.ValidateAddress(req.SourceAddress) != nil {
		return Alias{}, endpoint.Invalid(map[string]string{"source_address": "must be a valid email address"})
	}

	// Alias sources are hosted identities, like mailbox addresses. Parse away
	// display names and normalize the local identity before storing it so the
	// recipient checks can find aliases submitted in an equivalent spelling.
	// Destinations are remote addresses and must retain their original spelling.
	parsed, _ := mail.ParseAddress(req.SourceAddress) // validated above
	at := strings.LastIndexByte(parsed.Address, '@')
	domainName, err := rmail.DomainASCII(parsed.Address[at+1:])
	if err != nil {
		return Alias{}, endpoint.Invalid(map[string]string{"source_address": "must be a valid email address"})
	}
	domainName = strings.TrimSuffix(domainName, ".")
	parsed.Name = ""
	parsed.Address = norm.NFC.String(strings.ToLower(parsed.Address[:at])) + "@" + domainName
	// Address.String preserves quoting where the parsed local part needs it.
	req.SourceAddress = strings.TrimSuffix(strings.TrimPrefix(parsed.String(), "<"), ">")

	if !scopedDomainAllowed(r, middleware.CapMailboxesWrite, domainName) {
		return Alias{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "Insufficient permissions")
	}
	var domain models.Domain
	if err := h.db.Where("name = ?", domainName).First(&domain).Error; err != nil {
		return Alias{}, endpoint.Refuse(http.StatusNotFound, "domain_not_found", "Domain not found for source address")
	}

	alias := models.Alias{
		DomainID:           domain.ID,
		SourceAddress:      req.SourceAddress,
		DestinationAddress: req.DestinationAddress,
		Active:             true,
	}

	if err := h.db.Create(&alias).Error; err != nil {
		return Alias{}, endpoint.Refuse(http.StatusConflict, "already_exists", "Alias already exists")
	}

	return aliasOf(alias), nil
}

func (h *AliasHandler) get(r *http.Request, in adminAddressPath) (Alias, error) {
	id := in.ID

	var alias models.Alias
	if err := h.db.Preload("Domain").First(&alias, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Alias{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Alias not found")
		}
		return Alias{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to get alias")
	}

	redactAdminDomain(r, &alias.Domain)
	return aliasOf(alias), nil
}

type updateAliasRequest struct {
	DestinationAddress *string `json:"destination_address,omitempty"`
	Active             *bool   `json:"active,omitempty"`
}

func (h *AliasHandler) update(r *http.Request, in updateAliasInput) (Alias, error) {
	id := in.ID

	var alias models.Alias
	if err := h.db.First(&alias, id).Error; err != nil {
		return Alias{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Alias not found")
	}

	req := in.Body
	if req.DestinationAddress != nil && rmail.ValidateAddress(*req.DestinationAddress) != nil {
		return Alias{}, endpoint.Invalid(map[string]string{"destination_address": "must be a valid email address"})
	}

	updates := map[string]interface{}{}
	if req.DestinationAddress != nil {
		updates["destination_address"] = *req.DestinationAddress
	}
	if req.Active != nil {
		updates["active"] = *req.Active
	}

	if len(updates) > 0 {
		if err := h.db.Model(&alias).Updates(updates).Error; err != nil {
			return Alias{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to update alias")
		}
	}

	h.db.Preload("Domain").First(&alias, id)
	redactAdminDomain(r, &alias.Domain)
	return aliasOf(alias), nil
}

func (h *AliasHandler) delete(r *http.Request, in adminAddressPath) (endpoint.Empty, error) {
	id := in.ID

	var alias models.Alias
	if err := h.db.First(&alias, id).Error; err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusNotFound, "not_found", "Alias not found")
	}

	if err := h.db.Delete(&alias).Error; err != nil {
		return endpoint.Empty{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to delete alias")
	}

	return endpoint.Empty{}, nil
}
