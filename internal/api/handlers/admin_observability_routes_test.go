package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
)

func adminLogPass(string) endpoint.Middleware {
	return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
}
func banRoutes(h *BanHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, adminLogPass)
	return r
}
func logRoutes(h *LogHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, adminLogPass)
	return r
}
func auditRoutes(h *AuditHandler) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, withClaims(req, freshTwoFactorClaims(1, "auditor@example.test")))
		})
	})
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, AdminLogRouteChecks{At: adminLogPass, AuditRead: adminLogPass("")})
	return r
}
