package api

import (
	"net/http"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/respond"
)

// What the API's middleware adds to every operation it wraps, for the operations registered
// with endpoint.Register (#669, go-oapifly#25). Each sits beside the middleware it describes
// where the route groups are built in routes.go.

// securitySchemes are the ways a client authenticates, named as openapi.yaml names them.
var securitySchemes = map[string]oapifly.SecurityScheme{
	"CookieAuth": {Type: "apiKey", In: "cookie", Name: "restmail_access",
		Description: "httpOnly session cookie set by POST /api/v1/auth/login and /api/v1/auth/refresh. " +
			"State-changing requests (POST/PUT/PATCH/DELETE) additionally require the double-submit CSRF header."},
	"BasicAuth": {Type: "http", Scheme: "basic",
		Description: "A mailbox's address and one of its app passwords, as HTTP Basic. Accepted only by the operations that list it."},
	"BearerAuth": {Type: "http", Scheme: "bearer", BearerFormat: "JWT",
		Description: "JWT access token as an Authorization: Bearer header. Exempt from the CSRF check."},
	"APIKeyAuth": {Type: "http", Scheme: "bearer", BearerFormat: "rmk",
		Description: "A scoped API key (rmk_...) as an Authorization: Bearer header, allowed only where both the key's " +
			"permissions and its owner's live grants cover the capability at the target. Exempt from the CSRF check."},
}

// defaultSecurity is what an operation needs when nothing says otherwise.
var defaultSecurity = []oapifly.SecurityRequirement{{"CookieAuth": nil}, {"BearerAuth": nil}}

var errorBody = oapifly.TypeOf[respond.ErrorResponse]()

func refusal(status int, description string) oapifly.EndpointResponse {
	return oapifly.EndpointResponse{Status: status, Description: description, Type: errorBody}
}

// Several middleware answer 403, each with its own code, so they share one description.
var refusedBeforeTheHandler = refusal(http.StatusForbidden,
	"Refused before the handler runs: setup_required (the account has not finished setup), csrf_failed "+
		"(a cookie session's unsafe request without the CSRF header) or forbidden (the session lacks the capability)")

var (
	// describedAdminSession is the admin group's authentication: a session or a scoped API
	// key, never HTTP Basic, whose token is current.
	describedAdminSession = oapifly.Middleware{
		Security: []oapifly.SecurityRequirement{{"CookieAuth": nil}, {"BearerAuth": nil}, {"APIKeyAuth": nil}},
		Responses: []oapifly.EndpointResponse{
			refusal(http.StatusUnauthorized, "unauthorized: no token, a bad one, or a session that has ended"),
			refusal(http.StatusServiceUnavailable, "unavailable: the session or grants check could not be made"),
		},
	}
	describedSetupDone = oapifly.Middleware{Responses: []oapifly.EndpointResponse{refusedBeforeTheHandler}}
	describedCSRF      = oapifly.Middleware{Responses: []oapifly.EndpointResponse{refusedBeforeTheHandler}}
	describedBodyCap   = oapifly.Middleware{Responses: []oapifly.EndpointResponse{
		refusal(http.StatusRequestEntityTooLarge, "request_too_large: the body is over the API's size cap"),
	}}
)

// describedCapability makes a capability check a route is registered behind: it requires the
// capability as a role on every scheme, and answers 403 forbidden without it.
func describedCapability(check func(capability string) func(http.Handler) http.Handler) func(capability string) endpoint.Middleware {
	return func(capability string) endpoint.Middleware {
		return endpoint.Middleware{
			Wrap: check(capability),
			Describes: oapifly.Middleware{
				Roles:     []string{capability},
				Responses: []oapifly.EndpointResponse{refusedBeforeTheHandler},
			},
		}
	}
}

// Description is the API description generated from the registered routes.
func (rt *Routers) Description(version string) (map[string]interface{}, []string) {
	g := oapifly.New(oapifly.Config{
		Title:           "REST Mail API",
		Version:         version,
		SecuritySchemes: securitySchemes,
		Security:        defaultSecurity,
	})
	g.Register(rt.Registry.Endpoints()...)
	return g.Generate(), g.Warnings
}

// Resource checks resolve the mailbox or source domain before binding. A failed lookup
// answers 503; a missing or out-of-scope target answers the existing 403.
func describedResourceCapability(check func(string) func(http.Handler) http.Handler) func(string) endpoint.Middleware {
	return func(capability string) endpoint.Middleware {
		m := describedCapability(check)(capability)
		m.Describes.Responses = append(m.Describes.Responses,
			refusal(http.StatusServiceUnavailable, "unavailable: the resource scope could not be checked"))
		return m
	}
}
func describedMailboxCapability(check func(string) func(http.Handler) http.Handler) func(string) endpoint.Middleware {
	return describedResourceCapability(check)
}
func describedAliasCapability(check func(string) func(http.Handler) http.Handler) func(string) endpoint.Middleware {
	return describedResourceCapability(check)
}
