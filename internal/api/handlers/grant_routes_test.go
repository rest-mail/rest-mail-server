package handlers

import (
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func grantRoutes(h *GrantHandler) http.Handler {
	r := chi.NewRouter()
	pass := endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, GrantRouteChecks{
		At: func(string) endpoint.Middleware { return pass }, SessionOnly: pass, AuthThrottle: pass,
	})
	return r
}

// With no database configured, reaching storage would panic. These refusals must
// come from the registered route's binding and validation, before a transaction.
func TestRegisteredGrantValidation(t *testing.T) {
	otp := &auth.Claims{AccountID: 1, AMR: []string{auth.AMRPassword, auth.AMROTP}}
	password := &auth.Claims{AccountID: 1, AMR: []string{auth.AMRPassword}}
	for _, tc := range []struct {
		name, method, path, body string
		claims                   *auth.Claims
		status                   int
		code, field, message     string
	}{
		{name: "create two-factor before malformed body", method: "POST", path: "/api/v1/admin/grants", body: "broken", claims: password, status: 403, code: "two_factor_required"},
		{name: "create malformed body", method: "POST", path: "/api/v1/admin/grants", body: "broken", claims: otp, status: 400, code: "bad_request"},
		{name: "create array body", method: "POST", path: "/api/v1/admin/grants", body: "[]", claims: otp, status: 422, code: "validation_failed", field: "body"},
		{name: "create missing holder", method: "POST", path: "/api/v1/admin/grants", body: `{"role":"admin","scope":"*"}`, claims: otp, status: 422, code: "validation_failed", field: "holder", message: `required: {"kind": "account", "id": <account id>}`},
		{name: "create unsupported holder", method: "POST", path: "/api/v1/admin/grants", body: `{"holder":{"kind":"admin","id":1},"role":"admin","scope":"*"}`, claims: otp, status: 422, code: "validation_failed", field: "holder", message: "only accounts can be granted roles yet"},
		{name: "create role", method: "POST", path: "/api/v1/admin/grants", body: `{"holder":{"kind":"account","id":1},"scope":"*"}`, claims: otp, status: 422, code: "validation_failed", field: "role", message: "required: a role name"},
		{name: "create mode", method: "POST", path: "/api/v1/admin/grants", body: `{"holder":{"kind":"account","id":1},"role":"admin","scope":"*","mode":"other"}`, claims: otp, status: 422, code: "validation_failed", field: "mode", message: `must be "active" or "eligible"`},
		{name: "delete invalid id before two-factor", method: "DELETE", path: "/api/v1/admin/grants/0", claims: password, status: 422, code: "validation_failed", field: "id"},
		{name: "delete two-factor", method: "DELETE", path: "/api/v1/admin/grants/1", claims: password, status: 403, code: "two_factor_required"},
		{name: "activate empty reason", method: "POST", path: "/api/v1/admin/grants/1/activate", body: `{"reason":"  "}`, claims: password, status: 422, code: "validation_failed", field: "reason", message: "required: why you are switching it on"},
		{name: "activate minutes", method: "POST", path: "/api/v1/admin/grants/1/activate", body: `{"reason":"rotate keys","minutes":241}`, claims: password, status: 422, code: "validation_failed", field: "minutes", message: "must be from 1 to 240"},
		{name: "activate unknown field", method: "POST", path: "/api/v1/admin/grants/1/activate", body: `{"reason":"rotate keys","extra":true}`, claims: password, status: 422, code: "validation_failed", field: "extra"},
		{name: "activate malformed body", method: "POST", path: "/api/v1/admin/grants/1/activate", body: "broken", claims: password, status: 400, code: "bad_request"},
		{name: "make eligible malformed body", method: "POST", path: "/api/v1/admin/grants/1/make-eligible", body: "broken", claims: password, status: 400, code: "bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := withClaims(httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)), tc.claims)
			rr := httptest.NewRecorder()
			grantRoutes(NewGrantHandler(nil, nil, "")).ServeHTTP(rr, req)
			var body respond.ValidationErrorResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rr.Code != tc.status || body.Error.Code != tc.code {
				t.Fatalf("got %d %s", rr.Code, rr.Body.String())
			}
			if tc.field != "" {
				message := body.Error.Details.Fields[tc.field]
				if message == "" || (tc.message != "" && message != tc.message) {
					t.Fatalf("field %s = %q, want %q", tc.field, message, tc.message)
				}
			}
		})
	}
}
