package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rest-mail/rest-mail-server/internal/requestid"
	"github.com/rest-mail/rest-mail-server/internal/tracing/tracingtest"
)

const parentTrace = "4bf92f3577b34da6a3ce929d0e0e4736"

func tracedRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(RequestID, Tracing)
	r.Get("/api/v1/messages/{id}/raw", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	return r
}

// The server span is named by the route pattern, never the raw path, and
// carries the request ID; a verified gateway's traceparent is followed.
func TestTracing_NamesTheSpanByRouteAndFollowsAGateway(t *testing.T) {
	rec := tracingtest.Record(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/messages/42/raw", nil)
	r.TLS = gatewayTLS()
	r.Header.Set(requestid.Header, "gw-req-1")
	r.Header.Set("traceparent", "00-"+parentTrace+"-00f067aa0ba902b7-01")
	tracedRouter().ServeHTTP(httptest.NewRecorder(), r)

	span := tracingtest.WaitFor(t, rec, "GET /api/v1/messages/{id}/raw")
	if got := span.SpanContext().TraceID().String(); got != parentTrace {
		t.Errorf("trace = %s, want the gateway's %s", got, parentTrace)
	}
	if got := tracingtest.Attr(span, requestid.Attr); got != "gw-req-1" {
		t.Errorf("request_id = %q", got)
	}
	if got := tracingtest.Attr(span, "http.response.status_code"); got != "418" {
		t.Errorf("status = %q", got)
	}
}

// A public client's traceparent is not followed: it cannot pick the trace or
// force sampling, as it cannot pick the request ID.
func TestTracing_IgnoresAPublicClientsTraceparent(t *testing.T) {
	rec := tracingtest.Record(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/messages/42/raw", nil)
	r.Header.Set("traceparent", "00-"+parentTrace+"-00f067aa0ba902b7-01")
	tracedRouter().ServeHTTP(httptest.NewRecorder(), r)

	span := tracingtest.WaitFor(t, rec, "GET /api/v1/messages/{id}/raw")
	if span.SpanContext().TraceID().String() == parentTrace || span.Parent().IsValid() {
		t.Error("a public client's traceparent was followed")
	}
}
