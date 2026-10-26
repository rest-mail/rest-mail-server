package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/api/respond"
	apitypes "github.com/restmail/restmail/internal/api/types"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dns"
	"github.com/restmail/restmail/internal/grants"
	rmail "github.com/restmail/restmail/internal/mail"
	"github.com/restmail/restmail/internal/pipeline"
	"golang.org/x/net/idna"
	"gorm.io/gorm"
)

// domainNameProfile is a strict IDNA profile used to validate and normalize a
// user-supplied domain name before it is stored. It maps the input for lookup
// (case-folding, IDN → punycode), enforces the STD3 LDH rule and per-label
// validity, and applies the Bidi rule. A stored domain name flows into DNS
// lookups, MTA-STS host matching, sieve redirect allowlists and outbound routing,
// so only a resolvable hostname may be persisted.
var domainNameProfile = idna.New(
	idna.MapForLookup(),
	idna.BidiRule(),
	idna.ValidateLabels(true),
	idna.StrictDomainName(true),
)

// maxDomainNameLength is the RFC 1035 limit on a domain name's total length.
const maxDomainNameLength = 253

// validateDomainName normalizes and validates a user-supplied domain name,
// returning the canonical lowercase ASCII (A-label / punycode) form to store. It
// rejects empty input, over-long names, single-label hosts, and anything that is
// not a valid hostname. A valid internationalized name (e.g. "münchen.test") is
// accepted and returned in its ASCII-compatible encoding.
func validateDomainName(name string) (string, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" {
		return "", errors.New("domain name is required")
	}
	ascii, err := domainNameProfile.ToASCII(name)
	if err != nil {
		return "", fmt.Errorf("not a valid hostname: %w", err)
	}
	if len(ascii) > maxDomainNameLength {
		return "", errors.New("domain name exceeds the maximum length")
	}
	// A mail domain is a fully-qualified name, never a bare single-label host.
	if !strings.Contains(ascii, ".") {
		return "", errors.New("domain name must be fully-qualified")
	}
	return strings.ToLower(ascii), nil
}

type DomainHandler struct {
	db    *gorm.DB
	dns   dns.Provider
	cache *grants.Cache

	// The DNS check's lookups; tests replace them.
	lookupMX  func(string) ([]*net.MX, error)
	lookupTXT func(string) ([]string, error)
}

func NewDomainHandler(db *gorm.DB, dnsProvider dns.Provider, caches ...*grants.Cache) *DomainHandler {
	h := &DomainHandler{db: db, dns: dnsProvider, lookupMX: net.LookupMX, lookupTXT: net.LookupTXT}
	if len(caches) > 0 {
		h.cache = caches[0]
	}
	return h
}

// Domain is a domain as the API answers with it.
type Domain = apitypes.Domain

func domainOf(d models.Domain) Domain {
	return Domain{
		ID:                d.ID,
		Name:              d.Name,
		ServerType:        d.ServerType,
		Active:            d.Active,
		DefaultQuotaBytes: d.DefaultQuotaBytes,
		DKIMSelector:      d.DKIMSelector,
		SendingPolicy:     d.SendingPolicy,
		DMARCPolicy:       d.DMARCPolicy,
		CreatedAt:         d.CreatedAt,
		UpdatedAt:         d.UpdatedAt,
	}
}

// DomainRouteChecks are the capability checks the domain routes are registered behind, made
// by the router from the caller's grants.
type DomainRouteChecks struct {
	// At requires the capability at "*".
	At func(capability string) endpoint.Middleware
	// OnList requires it for a list, which then holds only what the grants reach.
	OnList func(capability string) endpoint.Middleware
	// OnDomain requires it on the domain the path's {id} names.
	OnDomain func(capability string) endpoint.Middleware
}

// Register mounts the domain routes on rs, each with the types it takes and answers (#645).
func (h *DomainHandler) Register(rs endpoint.Routes, checks DomainRouteChecks) {
	tags := []string{"Admin: Domains"}
	missing := []int{http.StatusNotFound}
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/domains",
		OperationID: "listDomains", Summary: "List all domains", Tags: tags,
		Middleware: []endpoint.Middleware{checks.OnList(middleware.CapDomainsRead)}}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/domains",
		OperationID: "createDomain", Summary: "Create a new domain", Tags: tags,
		Status: http.StatusCreated, Errors: []int{http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapDomainsWrite)}}, h.create)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/domains/{id}",
		OperationID: "getDomain", Summary: "Get a domain by ID", Tags: tags, Errors: missing,
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsRead)}}, h.get)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPatch, Path: "/api/v1/admin/domains/{id}",
		OperationID: "updateDomain", Summary: "Update a domain", Tags: tags,
		Errors:     []int{http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.update)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/domains/{id}",
		OperationID: "deleteDomain", Summary: "Delete a domain", Tags: tags,
		Description: domainDeletionDescription,
		Status:      http.StatusNoContent, Errors: []int{http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsDelete)}}, h.delete)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/domains/{id}/dns",
		OperationID: "domainDNSCheck", Summary: "Live DNS record check (MX, SPF, DMARC, DKIM, MTA-STS) for a domain",
		Tags: tags, Errors: missing,
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsRead)}}, h.dnsCheck)
}

// domainPath names a domain by the path's {id}.
type domainPath struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}

var errDomainNotFound = endpoint.Refuse(http.StatusNotFound, "domain_not_found", "Domain not found")

func (h *DomainHandler) find(id uint32) (models.Domain, error) {
	var domain models.Domain
	if err := h.db.First(&domain, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain, errDomainNotFound
		}
		return domain, err
	}
	return domain, nil
}

type listDomainsRequest struct {
	Limit  *int `query:"limit"`
	Offset *int `query:"offset"`
}

func (h *DomainHandler) list(r *http.Request, in listDomainsRequest) (endpoint.List[Domain], error) {
	limit, offset := pagination(in.Limit, in.Offset, defaultListLimit, maxListLimit)

	query := filterAdminList(h.db.Model(&models.Domain{}), r, middleware.CapDomainsRead, "domains")

	var total int64
	query.Count(&total)

	var rows []models.Domain
	if err := query.Order("name ASC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return endpoint.List[Domain]{}, fmt.Errorf("list domains: %w", err)
	}
	domains := make([]Domain, len(rows))
	for i, row := range rows {
		domains[i] = domainOf(row)
	}
	return endpoint.List[Domain]{
		Data:       domains,
		Pagination: &respond.Pagination{Total: total, HasMore: int64(offset+limit) < total},
	}, nil
}

// createDomainRequest creates a domain. Its sending policy is transactional, bulk, or empty
// for none (#144). Its DMARC policy is none unless the request asks for another (#729).
type createDomainRequest = apitypes.CreateDomainRequest

func (h *DomainHandler) create(_ *http.Request, req createDomainRequest) (Domain, error) {
	// Reject non-hostname input before it is inserted and normalize to the
	// canonical ASCII form (issue #202).
	normalized, err := validateDomainName(req.Name)
	if err != nil {
		return Domain{}, endpoint.Invalid(map[string]string{"name": "Invalid domain name: " + err.Error()})
	}
	req.Name = normalized

	serverType := "traditional"
	if req.ServerType != nil && *req.ServerType != "" {
		serverType = *req.ServerType
	}

	// A domain is set up in stages and is not live until it has a certificate of
	// its own: mail is only ever carried over TLS, and a live domain with no
	// certificate cannot be served (issue #289). Making it live is a separate
	// request, refused while there is no certificate to serve it with.
	domain := models.Domain{
		Name:          req.Name,
		ServerType:    serverType,
		Active:        false,
		SendingPolicy: req.SendingPolicy,
		DMARCPolicy:   models.DMARCPolicyNone,
	}
	if req.DMARCPolicy != nil {
		domain.DMARCPolicy = *req.DMARCPolicy
	}
	if req.DefaultQuotaBytes != nil {
		domain.DefaultQuotaBytes = *req.DefaultQuotaBytes
	}

	if err := h.db.Create(&domain).Error; err != nil {
		return Domain{}, endpoint.Refuse(http.StatusConflict, "already_exists", "Domain already exists")
	}

	// Create default inbound and outbound pipelines for the new domain.
	for _, direction := range []string{"inbound", "outbound"} {
		if err := h.db.Create(&models.Pipeline{
			DomainID:  domain.ID,
			Direction: direction,
			Filters:   pipeline.DefaultPipelineJSON(direction),
			Active:    true,
		}).Error; err != nil {
			slog.Error("failed to create default "+direction+" pipeline", "domain", domain.Name, "error", err)
		}
	}

	return domainOf(domain), nil
}

func (h *DomainHandler) get(_ *http.Request, in domainPath) (Domain, error) {
	domain, err := h.find(in.ID)
	if err != nil {
		return Domain{}, err
	}
	return domainOf(domain), nil
}

type updateDomainRequest struct {
	ID   uint32           `path:"id" minimum:"1" example:"1"`
	Body updateDomainBody `body:""`
}

// updateDomainBody changes what it names and leaves the rest. An empty sending policy clears it.
type updateDomainBody struct {
	Active            *bool   `json:"active,omitempty"`
	DefaultQuotaBytes *int64  `json:"default_quota_bytes,omitempty"`
	SendingPolicy     *string `json:"sending_policy,omitempty" enums:",transactional,bulk" message:"must be 'transactional', 'bulk' or empty"`
	DMARCPolicy       *string `json:"dmarc_policy,omitempty" enums:"none,quarantine,reject" message:"must be 'none', 'quarantine' or 'reject'"`
}

func (h *DomainHandler) update(_ *http.Request, in updateDomainRequest) (Domain, error) {
	domain, err := h.find(in.ID)
	if err != nil {
		return Domain{}, err
	}
	req := in.Body

	updates := map[string]interface{}{}
	if req.Active != nil {
		// A domain goes live only once it has a certificate of its own to be served
		// with: mail for it is only ever carried over TLS, and the gateways refuse
		// handshakes for a domain they cannot serve rather than answering under
		// another name (issues #289, #290). Taking one out of service is always
		// allowed.
		if *req.Active {
			var current int64
			if err := h.db.Model(&models.Certificate{}).
				Where("domain_id = ? AND not_before <= NOW() AND not_after > NOW()", domain.ID).
				Count(&current).Error; err != nil {
				return Domain{}, fmt.Errorf("check the domain's certificate: %w", err)
			}
			if current == 0 {
				return Domain{}, endpoint.Refuse(http.StatusConflict, "certificate_required",
					"This domain has no currently valid certificate, so it cannot go live. Add one first.")
			}
		}
		updates["active"] = *req.Active
	}
	if req.DefaultQuotaBytes != nil {
		updates["default_quota_bytes"] = *req.DefaultQuotaBytes
	}
	// An empty string clears the policy; it is a value here, not "unchanged",
	// so the map (not the struct) carries it to the update.
	if req.SendingPolicy != nil {
		updates["sending_policy"] = *req.SendingPolicy
	}
	if req.DMARCPolicy != nil {
		updates["dmarc_policy"] = *req.DMARCPolicy
	}

	if len(updates) > 0 {
		if err := h.db.Model(&domain).Updates(updates).Error; err != nil {
			return Domain{}, fmt.Errorf("update domain: %w", err)
		}
	}

	return h.get(nil, domainPath{ID: in.ID})
}

func (h *DomainHandler) delete(r *http.Request, in domainPath) (endpoint.Empty, error) {
	if err := h.deleteDomainData(r, in.ID); err != nil {
		return endpoint.Empty{}, err
	}
	if h.cache != nil {
		h.cache.Invalidate()
	}
	return endpoint.Empty{}, nil
}

// DomainDNSCheck is what a live check of a domain's DNS records found.
type DomainDNSCheck struct {
	Domain  string           `json:"domain"`
	Records []DNSRecordCheck `json:"records"`
	// Summary counts the records verified: "4/7 records verified".
	Summary string `json:"summary"`
}

// DNSRecordCheck is one record the check looked for.
type DNSRecordCheck struct {
	// Type is DOMAIN, MX, SPF, DMARC, DKIM, MTA-STS or TLS-RPT.
	Type string `json:"type"`
	Name string `json:"name"`
	// Status is ok, missing or error.
	Status  string `json:"status" enums:"ok,missing,error"`
	Value   string `json:"value,omitempty"`
	Message string `json:"message,omitempty"`
}

// dnsCheck verifies DNS records for a domain (MX, SPF, DKIM, DMARC, MTA-STS, TLS-RPT).
func (h *DomainHandler) dnsCheck(_ *http.Request, in domainPath) (DomainDNSCheck, error) {
	domain, err := h.find(in.ID)
	if err != nil {
		return DomainDNSCheck{}, err
	}

	type recordCheck = DNSRecordCheck
	var checks []recordCheck

	// DNS holds the A-label (punycode) form of an internationalized name: look
	// up, and report the owner names to publish, in that form. A stored name
	// that is not a valid DNS name is reported, not looked up.
	name, err := rmail.DomainASCII(domain.Name)
	if err != nil {
		return DomainDNSCheck{
			Domain: domain.Name,
			Records: []recordCheck{{
				Type: "DOMAIN", Name: domain.Name, Status: "error",
				Message: "The domain name is not a valid DNS name, so its records cannot be checked",
			}},
			Summary: "0/1 records verified",
		}, nil
	}

	// MX records
	mxs, err := h.lookupMX(name)
	if err != nil {
		checks = append(checks, recordCheck{
			Type: "MX", Name: name, Status: "missing",
			Message: fmt.Sprintf("MX lookup failed: %v", err),
		})
	} else if len(mxs) == 0 {
		checks = append(checks, recordCheck{
			Type: "MX", Name: name, Status: "missing",
			Message: "No MX records found",
		})
	} else {
		hosts := make([]string, len(mxs))
		for i, mx := range mxs {
			hosts[i] = fmt.Sprintf("%s (pri %d)", mx.Host, mx.Pref)
		}
		checks = append(checks, recordCheck{
			Type: "MX", Name: name, Status: "ok",
			Value: strings.Join(hosts, ", "),
		})
	}

	// SPF (TXT record)
	spfFound := false
	txts, err := h.lookupTXT(name)
	if err == nil {
		for _, txt := range txts {
			if strings.HasPrefix(txt, "v=spf1") {
				spfFound = true
				checks = append(checks, recordCheck{
					Type: "SPF", Name: name, Status: "ok",
					Value: txt,
				})
				break
			}
		}
	}
	if !spfFound {
		checks = append(checks, recordCheck{
			Type: "SPF", Name: name, Status: "missing",
			Message: "No SPF record (v=spf1...) found in TXT records",
		})
	}

	// DMARC
	dmarcFound := false
	dmarcTxts, err := h.lookupTXT("_dmarc." + name)
	if err == nil {
		for _, txt := range dmarcTxts {
			if strings.HasPrefix(txt, "v=DMARC1") {
				dmarcFound = true
				checks = append(checks, recordCheck{
					Type: "DMARC", Name: "_dmarc." + name, Status: "ok",
					Value: txt,
				})
				break
			}
		}
	}
	if !dmarcFound {
		checks = append(checks, recordCheck{
			Type: "DMARC", Name: "_dmarc." + name, Status: "missing",
			Message: "No DMARC record found",
		})
	}

	// DKIM, under the selector the domain signs with (domains.dkim_selector).
	if domain.DKIMSelector == "" || domain.DKIMPrivateKey == "" {
		checks = append(checks, recordCheck{
			Type: "DKIM", Name: name, Status: "missing",
			Message: "The domain has no DKIM key, so its mail is not signed",
		})
	} else {
		dkimName := domain.DKIMSelector + "._domainkey." + name
		dkimTxts, err := h.lookupTXT(dkimName)
		if err != nil || len(dkimTxts) == 0 {
			checks = append(checks, recordCheck{
				Type: "DKIM", Name: dkimName, Status: "missing",
				Message: fmt.Sprintf("No DKIM record found for selector %q", domain.DKIMSelector),
			})
		} else {
			checks = append(checks, recordCheck{
				Type: "DKIM", Name: dkimName, Status: "ok",
				Value: dkimTxts[0],
			})
		}
	}

	// MTA-STS
	mtaSTSName := "_mta-sts." + name
	mtaSTSTxts, err := h.lookupTXT(mtaSTSName)
	if err != nil || len(mtaSTSTxts) == 0 {
		checks = append(checks, recordCheck{
			Type: "MTA-STS", Name: mtaSTSName, Status: "missing",
			Message: "No MTA-STS TXT record found",
		})
	} else {
		found := false
		for _, txt := range mtaSTSTxts {
			if strings.HasPrefix(txt, "v=STSv1") {
				found = true
				checks = append(checks, recordCheck{
					Type: "MTA-STS", Name: mtaSTSName, Status: "ok",
					Value: txt,
				})
				break
			}
		}
		if !found {
			checks = append(checks, recordCheck{
				Type: "MTA-STS", Name: mtaSTSName, Status: "missing",
				Message: "TXT records exist but none start with v=STSv1",
			})
		}
	}

	// TLS-RPT
	tlsRPTName := "_smtp._tls." + name
	tlsRPTTxts, err := h.lookupTXT(tlsRPTName)
	if err != nil || len(tlsRPTTxts) == 0 {
		checks = append(checks, recordCheck{
			Type: "TLS-RPT", Name: tlsRPTName, Status: "missing",
			Message: "No TLS-RPT TXT record found",
		})
	} else {
		found := false
		for _, txt := range tlsRPTTxts {
			if strings.HasPrefix(txt, "v=TLSRPTv1") {
				found = true
				checks = append(checks, recordCheck{
					Type: "TLS-RPT", Name: tlsRPTName, Status: "ok",
					Value: txt,
				})
				break
			}
		}
		if !found {
			checks = append(checks, recordCheck{
				Type: "TLS-RPT", Name: tlsRPTName, Status: "missing",
				Message: "TXT records exist but none start with v=TLSRPTv1",
			})
		}
	}

	// Count results
	okCount := 0
	for _, c := range checks {
		if c.Status == "ok" {
			okCount++
		}
	}

	return DomainDNSCheck{
		Domain:  domain.Name,
		Records: checks,
		Summary: fmt.Sprintf("%d/%d records verified", okCount, len(checks)),
	}, nil
}
