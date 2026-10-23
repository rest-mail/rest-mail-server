package handlers

import (
	"net/http"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
)

// DomainPolicyRouteChecks preserves domain-scoped reads/writes and installation-wide checks.
type DomainPolicyRouteChecks struct {
	// OnDomain requires the capability on the domain named by {id}.
	OnDomain func(string) endpoint.Middleware
	// At requires the capability at "*".
	At func(string) endpoint.Middleware
}
type senderRulePath struct {
	ID  uint32 `path:"id" minimum:"1"`
	EID uint32 `path:"eid" minimum:"1"`
}
type suppressionPath struct {
	ID  uint32 `path:"id" minimum:"1"`
	SID uint32 `path:"sid" minimum:"1"`
}
type addSenderRuleInput struct {
	ID   uint32               `path:"id" minimum:"1"`
	Body addSenderRuleRequest `body:""`
}
type setMTASTSInput struct {
	ID   uint32        `path:"id" minimum:"1"`
	Body setMTASTSBody `body:""`
}
type setMTASTSBody struct {
	Mode    string `json:"mode,omitempty" enums:",none,testing,enforce" message:"Must be one of: none, testing, enforce"`
	MXHosts string `json:"mx_hosts,omitempty"`
	MaxAge  *int   `json:"max_age,omitempty" minimum:"0" message:"Must be a non-negative integer (seconds)"`
	Active  *bool  `json:"active,omitempty"`
}
type setRelayInput struct {
	ID   uint32       `path:"id" minimum:"1"`
	Body setRelayBody `body:""`
}
type setRelayBody struct {
	Transport string  `json:"transport" enums:"direct,relay" message:"Must be one of: direct, relay"`
	Host      string  `json:"host,omitempty"`
	Port      *int    `json:"port,omitempty" minimum:"1" maximum:"65535" message:"Must be between 1 and 65535"`
	TLSMode   *string `json:"tls_mode,omitempty" enums:"starttls,implicit" message:"Must be one of: starttls, implicit"`
	Username  string  `json:"username,omitempty" maxLength:"255" message:"Must be at most 255 characters with no control characters"`
	Password  *string `json:"password,omitempty"`
}

// Register mounts the typed domain mail policy routes.
func (h *SenderRuleHandler) Register(rs endpoint.Routes, checks DomainPolicyRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/domains/{id}/allowlist",
		OperationID: "listAllowlist", Summary: "List allowlist entries for a domain", Tags: []string{"Admin: Domain mail policy"},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsRead)}}, h.listAllowlist)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/domains/{id}/allowlist",
		OperationID: "addToAllowlist", Summary: "Add an allowlist entry", Tags: []string{"Admin: Domain mail policy"}, Status: http.StatusCreated, Errors: []int{http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.addToAllowlist)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/domains/{id}/allowlist/{eid}",
		OperationID: "removeFromAllowlist", Summary: "Remove an allowlist entry", Tags: []string{"Admin: Domain mail policy"}, Status: http.StatusNoContent, Errors: []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.removeFromAllowlist)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/domains/{id}/blocklist",
		OperationID: "listBlocklist", Summary: "List blocklist entries for a domain", Tags: []string{"Admin: Domain mail policy"},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsRead)}}, h.listBlocklist)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPost, Path: "/api/v1/admin/domains/{id}/blocklist",
		OperationID: "addToBlocklist", Summary: "Add a blocklist entry", Tags: []string{"Admin: Domain mail policy"}, Status: http.StatusCreated, Errors: []int{http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.addToBlocklist)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/domains/{id}/blocklist/{eid}",
		OperationID: "removeFromBlocklist", Summary: "Remove a blocklist entry", Tags: []string{"Admin: Domain mail policy"}, Status: http.StatusNoContent, Errors: []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.removeFromBlocklist)
}

// Register mounts the typed domain mail policy routes.
func (h *SuppressionHandler) Register(rs endpoint.Routes, checks DomainPolicyRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/domains/{id}/suppressions",
		OperationID: "listSuppressions", Summary: "List a domain’s suppressed recipients", Tags: []string{"Admin: Domain mail policy"},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsRead)}}, h.list)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/domains/{id}/suppressions/{sid}",
		OperationID: "clearSuppression", Summary: "Clear a suppression", Tags: []string{"Admin: Domain mail policy"}, Status: http.StatusNoContent, Errors: []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.delete)
}

// Register mounts the typed domain mail policy routes.
func (h *MTASTSHandler) Register(rs endpoint.Routes, checks DomainPolicyRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/domains/{id}/mta-sts",
		OperationID: "getMtaStsPolicy", Summary: "Get the MTA-STS policy for a domain", Tags: []string{"Admin: Domain mail policy"}, Errors: []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsRead)}}, h.getPolicy)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/admin/domains/{id}/mta-sts",
		OperationID: "setMtaStsPolicy", Summary: "Create or update the MTA-STS policy for a domain", Tags: []string{"Admin: Domain mail policy"}, Errors: []int{http.StatusNotFound},
		Status: http.StatusOK, SuccessStatuses: []int{http.StatusCreated},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.setPolicy)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodDelete, Path: "/api/v1/admin/domains/{id}/mta-sts",
		OperationID: "deleteMtaStsPolicy", Summary: "Delete the MTA-STS policy for a domain", Tags: []string{"Admin: Domain mail policy"}, Status: http.StatusNoContent, Errors: []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsWrite)}}, h.deletePolicy)
}

// Register mounts the typed domain mail policy routes.
func (h *RelayHandler) Register(rs endpoint.Routes, checks DomainPolicyRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/domains/{id}/relay",
		OperationID: "getDomainRelay", Summary: "Get the domain’s outbound transport", Tags: []string{"Admin: Domain mail policy"}, Errors: []int{http.StatusNotFound},
		Middleware: []endpoint.Middleware{checks.OnDomain(middleware.CapDomainsRead)}}, h.get)
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodPut, Path: "/api/v1/admin/domains/{id}/relay",
		OperationID: "setDomainRelay", Summary: "Set the domain’s outbound transport", Tags: []string{"Admin: Domain mail policy"}, Errors: []int{http.StatusNotFound, http.StatusConflict},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapDomainsWrite)}}, h.set)
}

// Register mounts the typed domain mail policy routes.
func (h *TLSReportHandler) Register(rs endpoint.Routes, checks DomainPolicyRouteChecks) {
	endpoint.Register(rs, endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/admin/tls-reports",
		OperationID: "listTlsReports", Summary: "List received TLS-RPT reports", Tags: []string{"Admin: Domain mail policy"},
		Middleware: []endpoint.Middleware{checks.At(middleware.CapDomainsRead), {Wrap: emptyTLSReportNumbers}}}, h.listReports)
}

// Empty numeric filters have always meant the default. Normalize them before typed binding,
// while a supplied empty policy_type is still refused.
func emptyTLSReportNumbers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		changed := false
		for _, name := range []string{"domain_id", "limit", "offset"} {
			if query.Has(name) && query.Get(name) == "" {
				query.Del(name)
				changed = true
			}
		}
		if changed {
			clone := r.Clone(r.Context())
			url := *r.URL
			url.RawQuery = query.Encode()
			clone.URL = &url
			r = clone
		}
		next.ServeHTTP(w, r)
	})
}
