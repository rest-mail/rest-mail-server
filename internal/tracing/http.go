package tracing

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// PathShape is path with every segment that is not a plain lowercase word or
// an API version (v1) replaced by {}: /api/v1/messages/42/raw becomes
// /api/v1/messages/{}/raw. A client span is named by it, so no id, address or
// query reaches a name.
func PathShape(path string) string {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if s != "" && !plainWord(s) && !apiVersion(s) {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

func plainWord(s string) bool {
	for j := 0; j < len(s); j++ {
		if c := s[j]; (c < 'a' || c > 'z') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func apiVersion(s string) bool {
	if len(s) < 2 || len(s) > 3 || s[0] != 'v' {
		return false
	}
	for j := 1; j < len(s); j++ {
		if s[j] < '0' || s[j] > '9' {
			return false
		}
	}
	return true
}

// Do sends req with client inside a client span under the request's context,
// carrying the span as a W3C traceparent header. requestID, when set, goes on
// the span. The span holds the method, the path's shape and the status only.
func Do(client *http.Client, req *http.Request, requestID string) (*http.Response, error) {
	name := req.Method + " " + PathShape(req.URL.Path)
	ctx, span := Tracer().Start(req.Context(), name, trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	if span.IsRecording() {
		span.SetAttributes(attribute.String("http.request.method", req.Method))
		if requestID != "" {
			span.SetAttributes(RequestIDKey.String(requestID))
		}
	}
	req = req.WithContext(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := client.Do(req)
	if err != nil {
		Fail(span, err)
		return resp, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode >= 500 {
		span.SetStatus(codes.Error, "")
	}
	return resp, nil
}

// ServerStart starts the server span for r. The parent is taken from r's
// traceparent header only when trusted is true — a call from a gateway the API
// verified — so a public client cannot choose a trace or force sampling. The
// span is named by method alone until ServerEnd names it by its route.
func ServerStart(r *http.Request, trusted bool) (*http.Request, trace.Span) {
	ctx := r.Context()
	if trusted {
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))
	}
	ctx, span := Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer))
	if span.IsRecording() {
		span.SetAttributes(attribute.String("http.request.method", r.Method))
	}
	return r.WithContext(ctx), span
}

// ServerEnd names span "METHOD route" by the matched route pattern (never the
// raw path, which holds ids), records the status and ends it. An unmatched
// request keeps the method alone as its name.
func ServerEnd(span trace.Span, method, route string, status int) {
	if span.IsRecording() {
		if route != "" {
			span.SetName(method + " " + route)
			span.SetAttributes(attribute.String("http.route", route))
		}
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= 500 {
			span.SetStatus(codes.Error, "")
		}
	}
	span.End()
}
