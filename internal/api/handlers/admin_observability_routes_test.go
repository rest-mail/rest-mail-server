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
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, AdminLogRouteChecks{At: adminLogPass, AuditRead: adminLogPass("")})
	return r
}
