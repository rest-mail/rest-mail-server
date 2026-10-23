package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
)

// passThrough is a middleware that lets every request through, standing in for the checks
// routes.go registers a route behind.
var passThrough = endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}

// passingSessionChecks are SessionRouteChecks that let every request through.
var passingSessionChecks = SessionRouteChecks{
	Throttle: passThrough,
	Audit:    func(string, ...middleware.AuditOption) endpoint.Middleware { return passThrough },
}

// accountRoutes serves h's account routes as the router registers them, behind checks that
// let every request through, for tests of the handlers themselves.
func accountRoutes(h *AccountHandler) http.Handler {
	r := chi.NewRouter()
	rs := endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}
	h.RegisterReads(rs)
	h.RegisterCreate(rs, passThrough)
	h.RegisterLinks(rs, passingSessionChecks)
	return r
}

// credentialRoutes serves h's app password and API key routes as the router registers them,
// behind checks that let every request through.
func credentialRoutes(h *CredentialHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, passingSessionChecks)
	return r
}
