package handlers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/crypto"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/dns"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
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
	idna.VerifyDNSLength(true),
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
	// masterKey decrypts stored certificate keys, as the gateways do.
	masterKey string

	// Capture the configured resolver and carry each HTTP request context into DNS.
	lookupMX    func(context.Context, string) ([]*net.MX, error)
	lookupTXT   func(context.Context, string) ([]string, error)
	lookupSRV   func(context.Context, string) ([]*net.SRV, error)
	lookupIPv4  func(context.Context, string) ([]net.IP, error)
	lookupAddr  func(context.Context, string) ([]string, error)
	lookupCNAME func(context.Context, string) (string, error)

	// What the DNS check needs to work out the records a domain must publish.
	publish domainPublishing
}

// SetMasterKey supplies the key stored certificate keys are encrypted with, so
// that making a domain live can check its certificate as the gateways load it.
func (h *DomainHandler) SetMasterKey(masterKey string) {
	h.masterKey = masterKey
}

// servableCertificate refuses to take a domain live unless its newest current
// certificate is one the gateways can serve it with: the private key is the
// certificate's own and the certificate names the domain. A row with current
// dates is not enough, since the gateways would refuse every handshake for a
// domain whose pair they cannot load (issues #289, #1704).
func (h *DomainHandler) servableCertificate(ctx context.Context, domain models.Domain) error {
	var certs []models.Certificate
	if err := h.db.WithContext(ctx).
		Where("domain_id = ? AND not_before <= NOW() AND not_after > NOW()", domain.ID).
		Order("not_after DESC").Find(&certs).Error; err != nil {
		return fmt.Errorf("check the domain's certificate: %w", err)
	}
	if len(certs) == 0 {
		return endpoint.Refuse(http.StatusConflict, "certificate_required",
			"This domain has no currently valid certificate, so it cannot go live. Add one first.")
	}
	keyPEM := certs[0].KeyPEM
	if h.masterKey != "" {
		decrypted, err := crypto.DecryptString(keyPEM, h.masterKey)
		if err != nil {
			return endpoint.Refuse(http.StatusConflict, "certificate_required",
				"This domain's certificate cannot be served: its private key cannot be read. Upload a new one first.")
		}
		keyPEM = decrypted
	}
	if err := certificateServes(certs[0].CertPEM, keyPEM, domain.Name); err != nil {
		return endpoint.Refuse(http.StatusConflict, "certificate_required",
			"This domain's certificate cannot be served: "+err.Error()+". Upload a new one first.")
	}
	return nil
}

func NewDomainHandler(db *gorm.DB, dnsProvider dns.Provider, caches ...*grants.Cache) *DomainHandler {
	resolver := net.DefaultResolver
	h := &DomainHandler{
		db: db, dns: dnsProvider,
		lookupMX:  absoluteLookup(resolver.LookupMX),
		lookupTXT: absoluteLookup(resolver.LookupTXT),
		lookupSRV: absoluteLookup(func(ctx context.Context, name string) ([]*net.SRV, error) {
			_, records, err := resolver.LookupSRV(ctx, "", "", name)
			return records, err
		}),
		lookupIPv4: absoluteLookup(func(ctx context.Context, host string) ([]net.IP, error) {
			return resolver.LookupIP(ctx, "ip4", host)
		}),
		lookupAddr:  resolver.LookupAddr,
		lookupCNAME: absoluteLookup(resolver.LookupCNAME),
	}
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
		OutboundStop:      outboundStopOf(d),
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
		Description: "Atomically creates the domain, its default pipelines and postmaster account. The account is active, receives mail, has the domain's default quota (1 GiB unless specified; explicit zero is unlimited), no credential or password, and no OwnerID. The creator receives the ordinary owner grant. No plan-limit exemption is added. An administrator may replace it with a working alias. " + postmasterRequiredDescription,
		Status:      http.StatusCreated, Errors: []int{http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapDomainsWrite)}}, h.create)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/domains/{id}",
		OperationID: "getDomain", Summary: "Get a domain by ID", Tags: tags, Errors: missing,
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsRead)}}, h.get)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPatch, Path: "/api/v1/admin/domains/{id}",
		OperationID: "updateDomain", Summary: "Update a domain", Tags: tags,
		Description: "Activation requires a receiving postmaster account or alias as well as a valid certificate. " + postmasterRequiredDescription,
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
		Middleware:  []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.update)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/domains/{id}",
		OperationID: "deleteDomain", Summary: "Delete a domain", Tags: tags,
		Description: domainDeletionDescription,
		Status:      http.StatusNoContent, Errors: []int{http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsDelete)}}, h.delete)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/domains/{id}/restore",
		OperationID: "restoreDomain", Summary: "Restore an archived domain", Tags: tags,
		Description: "Restores the domain, its automatically provisioned empty postmaster and archived live configuration with their original identities. Requires installation-wide domains:write and a human two-factor session. Existing name conflicts return 409. Grants are restored through the audited grant-change mechanism, within the caller's current authority; eligible grants require a new activation.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
		Middleware:  []endpoint.Middleware{checks.At(middleware.CapDomainsWrite)}}, h.restore)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/domains/{id}/dns",
		OperationID: "domainDNSCheck", Summary: "The DNS records a domain must publish, compared with a live lookup of each",
		Tags: tags, Errors: missing,
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsRead)}}, h.dnsCheck)
	h.registerOutboundStop(rs, checks)
}

// domainPath names a domain by the path's {id}.
type domainPath struct {
	ID uint32 `path:"id" minimum:"1" example:"1"`
}

var errDomainNotFound = endpoint.Refuse(http.StatusNotFound, "domain_not_found", "Domain not found")

func (h *DomainHandler) find(ctx context.Context, id uint32) (models.Domain, error) {
	var domain models.Domain
	if err := h.db.WithContext(ctx).First(&domain, id).Error; err != nil {
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

func (h *DomainHandler) create(r *http.Request, req createDomainRequest) (Domain, error) {
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
		Name:              req.Name,
		ServerType:        serverType,
		Active:            false,
		SendingPolicy:     req.SendingPolicy,
		DMARCPolicy:       models.DMARCPolicyNone,
		DefaultQuotaBytes: 1 << 30,
	}
	if req.DMARCPolicy != nil {
		domain.DMARCPolicy = *req.DMARCPolicy
	}
	if req.DefaultQuotaBytes != nil {
		domain.DefaultQuotaBytes = *req.DefaultQuotaBytes
	}

	// Preserve an explicit zero instead of replacing it with GORM's model
	// default. Omitted quotas retain the model's documented 1 GiB default.
	row := struct {
		models.Domain
		DefaultQuotaBytes *int64
	}{Domain: domain, DefaultQuotaBytes: &domain.DefaultQuotaBytes}
	if err := h.db.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("domains").Create(&row).Error; err != nil {
			if repositories.IsUniqueViolation(err) {
				return endpoint.Refuse(http.StatusConflict, "already_exists", "Domain already exists")
			}
			return fmt.Errorf("create domain: %w", err)
		}
		// A domain and its default pipelines are one provisioning operation.
		for _, direction := range []string{"inbound", "outbound"} {
			if err := tx.Create(&models.Pipeline{
				DomainID: row.ID, Direction: direction,
				Filters: pipeline.DefaultPipelineJSON(direction), Active: true,
			}).Error; err != nil {
				return fmt.Errorf("create default %s pipeline: %w", direction, err)
			}
		}
		return provisionPostmaster(tx, r, row.Domain)
	}); err != nil {
		return Domain{}, err
	}
	if h.cache != nil {
		h.cache.Invalidate()
	}
	return domainOf(row.Domain), nil
}

func (h *DomainHandler) get(r *http.Request, in domainPath) (Domain, error) {
	domain, err := h.find(r.Context(), in.ID)
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
	DefaultQuotaBytes *int64  `json:"default_quota_bytes,omitempty" minimum:"0" message:"must not be negative"`
	SendingPolicy     *string `json:"sending_policy,omitempty" enums:",transactional,bulk" message:"must be 'transactional', 'bulk' or empty"`
	DMARCPolicy       *string `json:"dmarc_policy,omitempty" enums:"none,quarantine,reject" message:"must be 'none', 'quarantine' or 'reject'"`
}

func (h *DomainHandler) update(r *http.Request, in updateDomainRequest) (Domain, error) {
	domain, err := h.find(r.Context(), in.ID)
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
			configured, err := postmasterConfigured(h.db.WithContext(r.Context()), domain)
			if err != nil {
				return Domain{}, err
			}
			if !configured {
				return Domain{}, errPostmasterRequired
			}
			if err := h.servableCertificate(r.Context(), domain); err != nil {
				return Domain{}, err
			}
		}
		updates["active"] = *req.Active
	}
	if req.DefaultQuotaBytes != nil {
		if quotaRaised(domain.DefaultQuotaBytes, *req.DefaultQuotaBytes) {
			if err := RequireTwoFactor(r); err != nil {
				return Domain{}, err
			}
		}
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
		err := preservePostmasters(h.db.WithContext(r.Context()), req.Active != nil, func(tx *gorm.DB) error {
			query := tx.Model(&domain)
			quotaProof := RequireTwoFactor(r)
			if req.DefaultQuotaBytes != nil && quotaProof != nil {
				query = query.Where("default_quota_bytes <= 0 OR (? > 0 AND default_quota_bytes >= ?)", *req.DefaultQuotaBytes, *req.DefaultQuotaBytes)
			}
			result := query.Updates(updates)
			if result.Error != nil {
				return fmt.Errorf("update domain: %w", result.Error)
			}
			if req.DefaultQuotaBytes != nil && quotaProof != nil && result.RowsAffected == 0 {
				return quotaProof
			}
			return nil
		})
		if err != nil {
			return Domain{}, err
		}
	}

	return h.get(r, domainPath{ID: in.ID})
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

// DomainDNSCheck is what a domain must publish in DNS, and what a live check found (#1703).
type DomainDNSCheck struct {
	Domain  string           `json:"domain"`
	Records []DNSRecordCheck `json:"records"`
	// Summary counts the records verified: "4/7 records verified".
	Summary string `json:"summary"`
}

// DNSRecordCheck is one record the domain must publish, and what the check found there.
type DNSRecordCheck struct {
	// Type is DOMAIN, MX, SPF, DMARC, DKIM, MTA-STS, CNAME (the mail app discovery names and MTA-STS's policy host), TLS-RPT,
	// SRV, A or PTR.
	Type string `json:"type"`
	Name string `json:"name"`
	// Status is ok when what is published matches expected, missing when nothing is, different
	// when it does not match, and error when it could not be compared.
	Status string `json:"status" enums:"ok,missing,different,error"`
	// Value is what the lookup found.
	Value string `json:"value,omitempty"`
	// Expected is the value to publish: the list `restmail-api dns` prints. It is empty when
	// the domain has nothing to publish here, or the server cannot say what it should be. On a
	// DKIM record it is the public half of the key the domain signs with, "v=DKIM1;
	// k=<rsa|ed25519>; p=<base64 public key>". An RSA value is longer than one 255-byte DNS
	// string; publish it as several strings of one record.
	Expected string `json:"expected,omitempty"`
	Message  string `json:"message,omitempty"`
	// Problem, on a DKIM record that is not ok, says what differs from Expected: missing, no
	// record there; malformed, no record there is a DKIM key record; different_key_type, a
	// record for another key type (k=); different_key, a record with another key, or a revoked
	// one (empty p=); more_than_one, the key is there among other records; key_unreadable, the
	// stored key cannot be read, so nothing is expected.
	Problem string `json:"problem,omitempty" enums:"missing,malformed,different_key_type,different_key,more_than_one,key_unreadable"`
}

// dnsCheck lists the records a domain must publish, with their values, and compares each with
// what a live lookup finds.
func (h *DomainHandler) dnsCheck(r *http.Request, in domainPath) (DomainDNSCheck, error) {
	domain, err := h.find(r.Context(), in.ID)
	if err != nil {
		return DomainDNSCheck{}, err
	}

	// DNS holds the A-label (punycode) form of an internationalized name: look
	// up, and report the owner names to publish, in that form. A stored name
	// that is not a valid DNS name is reported, not looked up.
	name, err := rmail.DomainASCII(domain.Name)
	if err != nil {
		return DomainDNSCheck{
			Domain: domain.Name,
			Records: []DNSRecordCheck{{
				Type: "DOMAIN", Name: domain.Name, Status: "error",
				Message: "The domain name is not a valid DNS name, so its records cannot be checked",
			}},
			Summary: "0/1 records verified",
		}, nil
	}

	checks, err := h.checkPublishedRecords(r.Context(), domain, name)
	if err != nil {
		return DomainDNSCheck{}, err
	}

	// Count results
	okCount := 0
	for _, c := range checks {
		if c.Status == statusOK {
			okCount++
		}
	}

	return DomainDNSCheck{
		Domain:  domain.Name,
		Records: checks,
		Summary: fmt.Sprintf("%d/%d records verified", okCount, len(checks)),
	}, nil
}
