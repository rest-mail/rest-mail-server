package smtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/requestid"
	"github.com/rest-mail/rest-mail-server/internal/tracing/tracingtest"
)

// passFilter is a pipeline filter that lets every message through.
type passFilter struct{}

func (passFilter) Name() string              { return "trace_pass" }
func (passFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (passFilter) Execute(context.Context, *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionContinue}, nil
}

// tracedAPI stands in for the API's internal listener: the real RequestID and
// Tracing middleware, a delivery route that runs a real pipeline engine, and
// the mailbox check. It marks each request as carrying a verified gateway
// certificate, which the internal listener's mTLS gives a real gateway (see
// apiclient's mtls tests); the middleware trusts the gateway's ID and trace
// only from such a request.
func tracedAPI(t *testing.T) *httptest.Server {
	t.Helper()
	reg := pipeline.NewRegistry()
	reg.Register("trace_pass", func([]byte) (pipeline.Filter, error) { return passFilter{}, nil })
	engine := pipeline.NewEngine(reg, nil)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cert := &x509.Certificate{}
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}
			next.ServeHTTP(w, r)
		})
	})
	r.Use(middleware.RequestID, middleware.Tracing)
	r.Get("/api/mailboxes", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"address": r.URL.Query().Get("address"), "exists": true, "mailbox_id": 7}})
	})
	r.Post("/api/v1/messages/deliver", func(w http.ResponseWriter, r *http.Request) {
		cfg := &pipeline.PipelineConfig{ID: 3, Direction: "inbound", Filters: []pipeline.FilterConfig{{Name: "trace_pass", Type: pipeline.FilterTypeAction, Enabled: true}}}
		if _, err := engine.Execute(r.Context(), cfg, &pipeline.EmailJSON{}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "processed"}})
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// One message through the SMTP gateway makes one connected trace (#1580): the
// connection's span, its calls to the API, the API's request span and the
// pipeline run under it share one trace and carry the connection's request
// ID, and no span holds an address.
func TestTracing_GatewayToAPIToPipelineIsOneTrace(t *testing.T) {
	rec := tracingtest.Record(t)
	api := tracedAPI(t)

	h := newSMTPHarnessWith(t, Clients{Delivery: apiclient.New(api.URL), Submission: apiclient.New(api.URL)}, newMockStore(), false, true)
	h.ehlo()
	if r := h.cmd("MAIL FROM:<sender@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT TO = %q", r)
	}
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q", final)
	}
	h.cmd("QUIT")

	callDeliver := waitForSpan(t, rec, func(s sdktrace.ReadOnlySpan) bool {
		return s.Name() == "POST /api/v1/messages/deliver" && s.SpanKind() == trace.SpanKindClient
	})
	// go-smtp starts a fresh session after STARTTLS, so the connection's
	// session span is the one the delivery call hangs under; it ends at QUIT.
	session := waitForSpan(t, rec, func(s sdktrace.ReadOnlySpan) bool {
		return s.Name() == "smtp.session" && s.SpanContext().SpanID() == callDeliver.Parent().SpanID()
	})
	callCheck := waitForSpan(t, rec, func(s sdktrace.ReadOnlySpan) bool {
		return s.Name() == "GET /api/mailboxes" && s.SpanKind() == trace.SpanKindClient && s.Parent().SpanID() == session.SpanContext().SpanID()
	})
	serveDeliver := waitForSpan(t, rec, func(s sdktrace.ReadOnlySpan) bool {
		return s.Name() == "POST /api/v1/messages/deliver" && s.SpanKind() == trace.SpanKindServer
	})
	run := tracingtest.WaitFor(t, rec, "pipeline.run")
	filter := tracingtest.WaitFor(t, rec, "pipeline.filter trace_pass")

	id := tracingtest.Attr(session, requestid.Attr)
	if !requestid.Valid(id) {
		t.Fatalf("session span request_id = %q", id)
	}
	traceID := session.SpanContext().TraceID()
	parentOf := map[string]struct{ child, parent sdktrace.ReadOnlySpan }{
		"gateway call (check) under the session":   {callCheck, session},
		"gateway call (deliver) under the session": {callDeliver, session},
		"API request under the gateway call":       {serveDeliver, callDeliver},
		"pipeline run under the API request":       {run, serveDeliver},
		"filter under the pipeline run":            {filter, run},
	}
	for what, p := range parentOf {
		if p.child.SpanContext().TraceID() != traceID {
			t.Errorf("%s: trace %s, want %s", what, p.child.SpanContext().TraceID(), traceID)
		}
		if p.child.Parent().SpanID() != p.parent.SpanContext().SpanID() {
			t.Errorf("%s: parent %s, want %s", what, p.child.Parent().SpanID(), p.parent.SpanContext().SpanID())
		}
	}
	for _, s := range []sdktrace.ReadOnlySpan{callCheck, callDeliver, serveDeliver, run} {
		if got := tracingtest.Attr(s, requestid.Attr); got != id {
			t.Errorf("span %q request_id = %q, want the connection's %q", s.Name(), got, id)
		}
	}
	if got := tracingtest.Attr(serveDeliver, "http.route"); got != "/api/v1/messages/deliver" {
		t.Errorf("http.route = %q", got)
	}

	// No attribute or name of any span holds an address.
	for _, s := range rec.Ended() {
		if strings.Contains(s.Name(), "@") {
			t.Errorf("span name %q holds an address", s.Name())
		}
		for _, kv := range s.Attributes() {
			if strings.Contains(kv.Value.String(), "@") {
				t.Errorf("span %q attribute %s = %q holds an address", s.Name(), kv.Key, kv.Value.String())
			}
		}
	}
}

// waitForSpan returns the first ended span match accepts, waiting up to thirty
// seconds for it.
func waitForSpan(t *testing.T, rec *tracetest.SpanRecorder, match func(sdktrace.ReadOnlySpan) bool) sdktrace.ReadOnlySpan {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, s := range rec.Ended() {
			if match(s) {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the span was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
