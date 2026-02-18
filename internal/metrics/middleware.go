package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

// Unwrap returns the underlying ResponseWriter for compatibility with
// http.ResponseController and middleware that check for interfaces like
// http.Flusher.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// HTTPMetrics is a chi-compatible middleware that records request count and
// duration for every HTTP request. Route patterns are normalised so that
// path parameters (e.g. /api/v1/messages/42) collapse into their template
// form (/api/v1/messages/{id}).
func HTTPMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := newResponseWriter(w)

		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(rw.statusCode)

		// Use the chi route pattern when available so that metrics are
		// grouped by route rather than by individual URL.
		path := normalisePath(r)

		HTTPRequestsTotal.WithLabelValues(r.Method, path, status).Inc()
		HTTPRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
	})
}

// normalisePath returns the chi route pattern for the request, falling back
// to the raw URL path. Long or unmatched paths are collapsed to "other" to
// prevent unbounded cardinality.
func normalisePath(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx != nil && rctx.RoutePattern() != "" {
		return rctx.RoutePattern()
	}

	// Fallback: use the first three segments to keep cardinality bounded.
	path := r.URL.Path
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 5)
	if len(parts) > 4 {
		return "/" + strings.Join(parts[:4], "/") + "/..."
	}
	return path
}
