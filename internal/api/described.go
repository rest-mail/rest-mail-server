package api

import (
	"net/http"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
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
	// key, never HTTP Basic, whose token is current. Refused API keys are throttled at
	// login's rate.
	describedAdminSession = oapifly.Middleware{
		Security: []oapifly.SecurityRequirement{{"CookieAuth": nil}, {"BearerAuth": nil}, {"APIKeyAuth": nil}},
		Responses: []oapifly.EndpointResponse{
			refusal(http.StatusUnauthorized, "unauthorized: no token, a bad one, or a session that has ended"),
			rateLimited("rate_limited: refused API keys, throttled at login's rate"),
			refusal(http.StatusServiceUnavailable, "unavailable: the session or grants check could not be made"),
		},
	}
	describedSetupDone = oapifly.Middleware{Responses: []oapifly.EndpointResponse{refusedBeforeTheHandler}}
	describedCSRF      = oapifly.Middleware{Responses: []oapifly.EndpointResponse{refusedBeforeTheHandler}}
	describedBodyCap   = oapifly.Middleware{Responses: []oapifly.EndpointResponse{
		refusal(http.StatusRequestEntityTooLarge, "request_too_large: the body is over the API's size cap"),
	}}
)

var (
	// describedPersonSession is the authentication of the routes that need a person: a
	// session from signing in, never a scoped API key or HTTP Basic, whose token is current.
	describedPersonSession = oapifly.Middleware{
		Security: defaultSecurity,
		Responses: []oapifly.EndpointResponse{
			refusal(http.StatusUnauthorized, "unauthorized: no token, a bad one, or a session that has ended"),
			refusal(http.StatusServiceUnavailable, "unavailable: the session check could not be made"),
		},
	}
	// describedMailSession is the mail routes' authentication: a session, a scoped API key,
	// or a credential sent with HTTP Basic, whose refusals are throttled at login's rate.
	describedMailSession = oapifly.Middleware{
		Security: []oapifly.SecurityRequirement{{"CookieAuth": nil}, {"BearerAuth": nil}, {"APIKeyAuth": nil}, {"BasicAuth": nil}},
		Responses: []oapifly.EndpointResponse{
			refusal(http.StatusUnauthorized, "unauthorized, app_password_required or setup_required: a refused session or HTTP Basic credential"),
			rateLimited("rate_limited: refused API keys or HTTP Basic credentials, throttled at login's rate"),
			refusal(http.StatusServiceUnavailable, "unavailable: the credential or session check could not be made"),
		},
	}
)

// rateLimited is a 429 with the Retry-After seconds the limiter writes.
func rateLimited(description string) oapifly.EndpointResponse {
	limited := refusal(http.StatusTooManyRequests, description)
	limited.Headers = map[string]oapifly.ResponseHeader{
		"Retry-After": {Description: "Seconds until a request may be made again", Required: true, Type: oapifly.TypeOf[int]()},
	}
	return limited
}

// describedThrottle is login's per-client-IP limit on a route that checks a password.
func describedThrottle(wrap func(http.Handler) http.Handler) endpoint.Middleware {
	return endpoint.Middleware{Wrap: wrap, Describes: oapifly.Middleware{Responses: []oapifly.EndpointResponse{
		rateLimited("rate_limited: too many requests to the shared authentication throttle"),
	}}}
}

// describedOwnerThrottle is the owner endpoints' per-client-IP limit: login's rate, in a
// bucket of its own.
func describedOwnerThrottle(wrap func(http.Handler) http.Handler) endpoint.Middleware {
	return endpoint.Middleware{Wrap: wrap, Describes: oapifly.Middleware{Responses: []oapifly.EndpointResponse{
		rateLimited("rate_limited: too many requests to the owner endpoints' throttle"),
	}}}
}

// describedTLSRPTThrottle is TLS-RPT ingestion's per-IP rate limit (#183).
func describedTLSRPTThrottle(wrap func(http.Handler) http.Handler) endpoint.Middleware {
	return endpoint.Middleware{Wrap: wrap, Describes: oapifly.Middleware{Responses: []oapifly.EndpointResponse{
		rateLimited("rate_limited: too many reports from one address"),
	}}}
}

// describedAudit records a route's action in the audit log. Recording does not change the
// response or the authentication requirement.
func describedAudit(audit func(action string, options ...middleware.AuditOption) func(http.Handler) http.Handler) func(string, ...middleware.AuditOption) endpoint.Middleware {
	return func(action string, options ...middleware.AuditOption) endpoint.Middleware {
		return endpoint.Middleware{Wrap: audit(action, options...)}
	}
}

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

// Description is the API description generated from the registered routes, with the
// example pairs merged in, and the warnings about the routes.
func (rt *Routers) Description(version string) (map[string]interface{}, []string) {
	g := rt.generator(version)
	doc := g.Generate()
	var warnings []string
	for _, w := range g.Report() {
		if !exampleKinds[w.Kind] {
			warnings = append(warnings, w.Message)
		}
	}
	return doc, warnings
}

// exampleKinds are the warnings about openapi-example.json, which its own test holds to
// the description (examples_test.go), rather than the description's.
var exampleKinds = map[oapifly.WarningKind]bool{
	oapifly.WarnExamplesUnreadable: true,
	oapifly.WarnExampleOrphaned:    true,
	oapifly.WarnExampleMissing:     true,
	oapifly.WarnExampleInvalid:     true,
}

// generator is the generator of the API description: the registered routes, and the
// example pairs in openapi-example.json (#625).
func (rt *Routers) generator(version string) *oapifly.Generator {
	return describe(rt.Registry, version)
}

// describe is the generator of the description of the routes registry holds, with the
// example pairs in openapi-example.json, as /api/docs/openapi.json serves it.
func describe(registry *endpoint.Registry, version string) *oapifly.Generator {
	g := oapifly.New(oapifly.Config{
		Title:           "REST Mail API",
		Version:         version,
		SecuritySchemes: securitySchemes,
		Security:        defaultSecurity,
		Examples:        restmail.Examples,
		ExamplesFile:    "openapi-example.json",
		Strict:          strictAreaScope(),

		ExampleRequirements: exampleRequirements,
	})
	g.Register(withResponseLinks(registry.Endpoints())...)
	return g
}

// exampleRequirements is what an example pair may say proving it needs (#1236): the
// principal it is sent as, a body the description cannot hold, a limit spent, a
// dependency fault, or state the seeded fixtures do not have (an authenticator, an
// owner key, a grant in a given mode, a pipeline that discards). A pair that requires
// none of these runs as the fixture admin against the seeded data, which is what the
// live contract run provides.
var exampleRequirements = map[string][]string{
	"principal": {"anonymous", "without_capability"},
	"body":      {"over_cap", "malformed", "compressed", "unsupported_media"},
	"limit":     {"rate", "message_size"},
	"fault":     {"database", "database_pool", "session_store", "permission_store", "context_cancelled", "sidecar_response", "error_adapter"},
	"state": {
		"active_grant", "active_policy", "calendar_invitation", "discarding_pipeline", "eligible_grant",
		"enrolled_authenticator", "enrollment_disabled", "event_stream", "existing_grant", "existing_link", "existing_policy", "existing_script",
		"full_mailbox", "future_time", "hosted_restmail_domain", "known_password", "mail_host_unconfigured", "own_account", "owner_key",
		"pending_enrollment", "pending_recovery_email", "quarantined_message", "recovery_email_token",
		"two_factor_off", "two_factor_session", "verified_recovery_email",
	},
}

// The preview limit applies per IP before binding or running the engine.
var describedPipelineTestThrottle = oapifly.Middleware{Responses: []oapifly.EndpointResponse{
	{Status: http.StatusTooManyRequests, Description: "rate_limited: pipeline preview limit exceeded", Type: errorBody, Headers: map[string]oapifly.ResponseHeader{"Retry-After": {Description: "Seconds until another preview is allowed", Required: true, Type: oapifly.TypeOf[int]()}}},
}}

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

// Grant changes and escalation require a person's session, never a machine key.
var describedSessionOnly = oapifly.Middleware{
	Security: []oapifly.SecurityRequirement{{"CookieAuth": nil}, {"BearerAuth": nil}},
	Responses: []oapifly.EndpointResponse{
		refusal(http.StatusUnauthorized, "unauthorized: authentication required"),
		refusal(http.StatusForbidden, "session_required: sign in with your password to manage eligible permissions"),
	},
}

var describedAuthThrottle = oapifly.Middleware{Responses: []oapifly.EndpointResponse{
	{Status: http.StatusTooManyRequests, Description: "rate_limited: login's per-IP limit", Type: errorBody,
		Headers: map[string]oapifly.ResponseHeader{"Retry-After": {Description: "Seconds until another try is allowed", Required: true, Type: oapifly.TypeOf[int]()}}},
}}

// describedAuditRead records every audit read, including a capability refusal.
// Recording does not change the response or the authentication requirement.
func describedAuditRead(wrap func(http.Handler) http.Handler) endpoint.Middleware {
	return endpoint.Middleware{Wrap: wrap}
}

// describedSecurityHeaders is attached only to the listener that installs the middleware.
func describedSecurityHeaders(cfg middleware.SecurityHeadersConfig) func(string) map[string]oapifly.ResponseHeader {
	return func(path string) map[string]oapifly.ResponseHeader {
		headers := map[string]oapifly.ResponseHeader{}
		for name, values := range middleware.SecurityHeaderValues(cfg, path) {
			headers[name] = oapifly.ResponseHeader{Description: values[0], Required: true}
		}
		return headers
	}
}
