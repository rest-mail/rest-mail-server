package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/rest-mail/rest-mail-server/internal/tracing"
)

// Tracing makes one server span per request (#1580), named by the matched
// route pattern and tagged with the request ID, so it must run after
// RequestID. A traceparent header is followed only from a gateway the API
// verified on the internal listener, the rule RequestID uses for its header.
// With tracing off the span is OpenTelemetry's no-op one and the response is
// not wrapped.
func Tracing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, span := tracing.ServerStart(r, verifiedClientCert(r))
		if !span.IsRecording() {
			next.ServeHTTP(w, r)
			span.End()
			return
		}
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			route := ""
			if rc := chi.RouteContext(r.Context()); rc != nil {
				route = rc.RoutePattern()
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			tracing.ServerEnd(span, r.Method, route, status)
		}()
		next.ServeHTTP(ww, r)
	})
}
