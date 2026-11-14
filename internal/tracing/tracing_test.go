package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// restoreGlobals puts the no-op provider back when the test ends.
func restoreGlobals(t *testing.T) {
	t.Cleanup(func() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	})
}

// clearOTelEnv unsets every variable that could switch tracing on.
func clearOTelEnv(t *testing.T) {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_SDK_DISABLED"} {
		t.Setenv(k, "")
	}
}

// With no endpoint configured, Setup installs nothing: spans are the no-op
// tracer's, so nothing is recorded, exported or sent anywhere — even with the
// other OTEL_* variables set.
func TestSetup_OffWithoutAnEndpoint(t *testing.T) {
	restoreGlobals(t)
	clearOTelEnv(t)
	t.Setenv("OTEL_SERVICE_NAME", "rest-mail-test")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	otel.SetTracerProvider(noop.NewTracerProvider())

	if Enabled() {
		t.Fatal("Enabled() with no endpoint")
	}
	shutdown, err := Setup(context.Background(), "rest-mail-api")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		t.Fatal("Setup installed the SDK provider with tracing off")
	}
	_, span := Start(context.Background(), "off")
	if span.IsRecording() || span.SpanContext().IsValid() {
		t.Fatal("a span is recorded with tracing off")
	}
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// OTEL_SDK_DISABLED=true keeps it off even with an endpoint.
func TestSetup_SDKDisabledWins(t *testing.T) {
	clearOTelEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	if Enabled() {
		t.Fatal("Enabled() with OTEL_SDK_DISABLED=true")
	}
}

// The positive control for the test above: with the endpoint set, spans are
// exported over OTLP/HTTP to it, named by OTEL_SERVICE_NAME's service.
func TestSetup_ExportsToTheConfiguredEndpoint(t *testing.T) {
	restoreGlobals(t)
	clearOTelEnv(t)
	var posts atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/traces" {
			posts.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")

	shutdown, err := Setup(context.Background(), "rest-mail-api")
	if err != nil {
		t.Fatal(err)
	}
	_, span := Start(context.Background(), "on")
	if !span.IsRecording() {
		t.Fatal("span not recorded with tracing on")
	}
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if posts.Load() == 0 {
		t.Fatal("no spans were exported to the configured endpoint")
	}
}

func TestPathShape(t *testing.T) {
	for in, want := range map[string]string{
		"/api/v1/messages/42/raw":             "/api/v1/messages/{}/raw",
		"/api/mailboxes?address=bob@x.test":   "/api/mailboxes",
		"/api/v1/accounts/bob@x.test/folders": "/api/v1/accounts/{}/folders",
		"/api/v1/gateway-sessions":            "/api/v1/gateway-sessions",
		"/api/v1/folders/INBOX":               "/api/v1/folders/{}",
	} {
		if got := PathShape(in); got != want {
			t.Errorf("PathShape(%q) = %q, want %q", in, got, want)
		}
	}
}
