package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
)

func pipelinePass(next http.Handler) http.Handler { return next }
func pipelineRoutes(h *PipelineHandler) http.Handler {
	r := chi.NewRouter()
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, PipelineRouteChecks{
		At:           func(string) endpoint.Middleware { return endpoint.Middleware{Wrap: pipelinePass} },
		TestThrottle: endpoint.Middleware{Wrap: pipelinePass},
	})
	return r
}

// Older shared query tests pass synthetic paths and the id in chi's context.
// Dispatch those probes through their actual registered route.
func pipelineRoute(h *PipelineHandler, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Path = strings.ReplaceAll(path, "{id}", chi.URLParam(r, "id"))
		clone.URL = &u
		pipelineRoutes(h).ServeHTTP(w, clone)
	}
}
func analyticsRoutes(h *StatsHandler) http.Handler {
	r := chi.NewRouter()
	h.RegisterPipelineAnalytics(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, endpoint.Middleware{Wrap: pipelinePass})
	return r
}

func pipelineValidation(fields map[string]string) string {
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "validation_failed", "message": "Request body failed validation", "details": map[string]any{"fields": fields}}})
	return string(raw)
}
