package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
)

type policyRegistrar interface {
	Register(endpoint.Routes, DomainPolicyRouteChecks)
}

func policyRoutes(h policyRegistrar) http.Handler {
	r := chi.NewRouter()
	pass := func(string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	}
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, DomainPolicyRouteChecks{At: pass, OnDomain: pass})
	return r
}

// Each middleware must check the named domain except the two installation-wide routes.
// Refusing before binding proves that the registered route actually executes that check.
func TestDomainPolicyRouteScopes(t *testing.T) {
	for _, tc := range []struct {
		handler                         policyRegistrar
		method, path, scope, capability string
	}{
		{NewSenderRuleHandler(nil), "GET", "/api/v1/admin/domains/7/allowlist", "domain", "domains:read"},
		{NewSenderRuleHandler(nil), "POST", "/api/v1/admin/domains/7/allowlist", "domain", "domains:write"},
		{NewSenderRuleHandler(nil), "DELETE", "/api/v1/admin/domains/7/allowlist/8", "domain", "domains:write"},
		{NewSenderRuleHandler(nil), "GET", "/api/v1/admin/domains/7/blocklist", "domain", "domains:read"},
		{NewSenderRuleHandler(nil), "POST", "/api/v1/admin/domains/7/blocklist", "domain", "domains:write"},
		{NewSenderRuleHandler(nil), "DELETE", "/api/v1/admin/domains/7/blocklist/8", "domain", "domains:write"},
		{NewSuppressionHandler(nil), "GET", "/api/v1/admin/domains/7/suppressions", "domain", "domains:read"},
		{NewSuppressionHandler(nil), "DELETE", "/api/v1/admin/domains/7/suppressions/8", "domain", "domains:write"},
		{NewMTASTSHandler(nil), "GET", "/api/v1/admin/domains/7/mta-sts", "domain", "domains:read"},
		{NewMTASTSHandler(nil), "PUT", "/api/v1/admin/domains/7/mta-sts", "domain", "domains:write"},
		{NewMTASTSHandler(nil), "DELETE", "/api/v1/admin/domains/7/mta-sts", "domain", "domains:write"},
		{NewRelayHandler(nil, ""), "GET", "/api/v1/admin/domains/7/relay", "domain", "domains:read"},
		{NewRelayHandler(nil, ""), "PUT", "/api/v1/admin/domains/7/relay", "all", "domains:write"},
		{NewTLSReportHandler(nil), "GET", "/api/v1/admin/tls-reports?domain_id=7", "all", "domains:read"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := chi.NewRouter()
			reg := endpoint.NewRegistry()
			check := func(scope string) func(string) endpoint.Middleware {
				return func(capability string) endpoint.Middleware {
					return endpoint.Middleware{Wrap: func(http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if scope != tc.scope || capability != tc.capability {
								t.Errorf("check %s %s, want %s %s", scope, capability, tc.scope, tc.capability)
							}
							if scope == "domain" && chi.URLParam(r, "id") != "7" {
								t.Error("domain check lost its path ID")
							}
							w.WriteHeader(http.StatusForbidden)
						})
					}}
				}
			}
			tc.handler.Register(endpoint.Routes{Router: r, Registry: reg}, DomainPolicyRouteChecks{At: check("all"), OnDomain: check("domain")})
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != http.StatusForbidden {
				t.Fatalf("got %d, want capability refusal", rr.Code)
			}
		})
	}
}

// Compare wire JSON, including omission and null behavior, against the former response
// values. The API types must not change existing clients' successful responses.
func TestDomainPolicyResponseJSONCompatibility(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	id := uint(9)
	domain := models.Domain{ID: 7, Name: "example.test", ServerType: "traditional", SendingPolicy: "bulk", CreatedAt: now, UpdatedAt: now}
	for _, populated := range []bool{false, true} {
		sender := models.DomainSenderRule{}
		suppression := models.Suppression{}
		policy := models.MTASTSPolicy{}
		report := models.TLSReport{}
		if populated {
			sender = models.DomainSenderRule{ID: 1, DomainID: 7, Pattern: "@example.test", ListType: "allow", Reason: "partner", CreatedBy: &id, CreatedAt: now}
			suppression = models.Suppression{ID: 2, DomainID: 7, Address: "gone@example.test", Reason: models.SuppressionHardBounce, FailureCategory: models.FailureBadMailbox, LastError: "550 gone", MessageID: &id, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
			policy = models.MTASTSPolicy{ID: 3, DomainID: 7, Mode: "testing", MXHosts: "mx.example.test", MaxAge: 604800, Active: true, CreatedAt: now, UpdatedAt: now, Domain: domain}
			report = models.TLSReport{ID: 4, DomainID: 7, ReportingOrg: "reporter", StartDate: now, EndDate: now, PolicyType: "sts", PolicyDomain: "example.test", TotalSuccessful: 10, TotalFailure: 1, FailureDetails: json.RawMessage(`[{"reason":"timeout"}]`), RawReport: `{"policies":[]}`, ReceivedAt: now, CreatedAt: now, Domain: domain}
		}
		for _, pair := range []struct{ old, typed interface{} }{
			{sender, senderRuleOf(sender)}, {suppression, suppressionOf(suppression)}, {policy, mtaSTSPolicyOf(policy)}, {report, tlsReportOf(report)},
		} {
			old, err := json.Marshal(pair.old)
			if err != nil {
				t.Fatal(err)
			}
			typed, err := json.Marshal(pair.typed)
			if err != nil {
				t.Fatal(err)
			}
			if string(old) != string(typed) {
				t.Errorf("%T wire JSON changed:\nold %s\nnew %s", pair.typed, old, typed)
			}
		}
	}
	rel := models.DomainRelay{DomainID: 7, Transport: "relay", Host: "smtp.example.test", Port: 587, TLSMode: "starttls", Username: "user", PasswordEnc: "encrypted-secret"}
	data, err := json.Marshal(viewRelay(7, &rel))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"domain_id":7,"transport":"relay","host":"smtp.example.test","port":587,"tls_mode":"starttls","username":"user","password_set":true}`
	if string(data) != want {
		t.Fatalf("relay response = %s, want %s", data, want)
	}
}

func TestDomainPolicyBindingRefusesBeforeDatabase(t *testing.T) {
	for _, tc := range []struct {
		handler                   policyRegistrar
		method, path, body, field string
		status                    int
	}{
		{NewRelayHandler(nil, ""), "PUT", "/api/v1/admin/domains/1/relay", `{"transport":"direct","port":0}`, "port", 422},
		{NewRelayHandler(nil, ""), "PUT", "/api/v1/admin/domains/1/relay", `{"transport":"direct","tls_mode":"none"}`, "tls_mode", 422},
		{NewRelayHandler(nil, ""), "PUT", "/api/v1/admin/domains/1/relay", `{"transport":"invalid"}`, "transport", 422},
		{NewRelayHandler(nil, ""), "PUT", "/api/v1/admin/domains/1/relay", `{"transport":"direct","port":"587"}`, "port", 422},
		{NewRelayHandler(nil, ""), "PUT", "/api/v1/admin/domains/1/relay", `[]`, "body", 422},
		{NewTLSReportHandler(nil), "GET", "/api/v1/admin/tls-reports?domain_id=not-a-number", "", "domain_id", 422},
		{NewTLSReportHandler(nil), "GET", "/api/v1/admin/tls-reports?limit=201", "", "limit", 422},
		{NewTLSReportHandler(nil), "GET", "/api/v1/admin/tls-reports?offset=1.5", "", "offset", 422},
	} {
		t.Run(tc.method+" "+tc.path+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			policyRoutes(tc.handler).ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != tc.status {
				t.Fatalf("got %d %s, want %d", rr.Code, rr.Body, tc.status)
			}
			if tc.field != "" {
				var body struct {
					Error struct {
						Code    string `json:"code"`
						Details struct {
							Fields map[string]string `json:"fields"`
						} `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Error.Code != "validation_failed" || body.Error.Details.Fields[tc.field] == "" {
					t.Errorf("missing %s validation error: %s", tc.field, rr.Body)
				}
			}
		})
	}
}
