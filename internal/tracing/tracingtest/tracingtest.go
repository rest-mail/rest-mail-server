// Package tracingtest records the spans a test makes, in memory.
package tracingtest

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/rest-mail/rest-mail-server/internal/tracing"
)

// Record installs a tracer provider that samples every span into the returned
// recorder, and puts the no-op provider back when the test ends. It changes
// process-wide state, so a test that calls it must not run in parallel.
func Record(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(rec))
	tracing.Install(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
		_ = tp.Shutdown(context.Background())
	})
	return rec
}

// WaitFor returns the first ended span named name, waiting up to five seconds
// for it to end.
func WaitFor(t *testing.T, rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, s := range rec.Ended() {
			if s.Name() == name {
				return s
			}
		}
		if time.Now().After(deadline) {
			names := []string{}
			for _, s := range rec.Ended() {
				names = append(names, s.Name())
			}
			t.Fatalf("no span %q ended; ended: %v", name, names)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Attr returns span's string attribute key, or "" when it has none.
func Attr(span sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.String()
		}
	}
	return ""
}
