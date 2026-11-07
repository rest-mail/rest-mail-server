// Package tracing is REST Mail's distributed tracing (#1580): OpenTelemetry
// spans for the API's HTTP handling, each gateway connection and its calls to
// the API, and the pipeline run and its filters, exported over OTLP/HTTP.
//
// It is off unless configured. With neither OTEL_EXPORTER_OTLP_ENDPOINT nor
// OTEL_EXPORTER_OTLP_TRACES_ENDPOINT set, Setup installs nothing: the global
// tracer stays OpenTelemetry's no-op one, no exporter is built and nothing
// dials out. When set, the standard OTEL_* variables apply as the SDK reads
// them: OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES name the service,
// OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG pick the sampler, and
// OTEL_EXPORTER_OTLP_HEADERS / _TIMEOUT / _INSECURE / _CERTIFICATE shape the
// export. OTEL_SDK_DISABLED=true turns it off again.
//
// Spans carry ids, counts, outcomes and durations only: never an address,
// subject, body or token. What logs and traces may hold about people is the
// owner's decision (#1580), so nothing personal goes on a span until it is made.
package tracing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/rest-mail/rest-mail-server/internal/requestid"
)

// Name is the instrumentation scope every REST Mail span is made under.
const Name = "github.com/rest-mail/rest-mail-server"

// RequestIDKey is the span attribute that holds the request ID, the same name
// the log lines use, so a trace and its log lines join on it.
const RequestIDKey = attribute.Key(requestid.Attr)

// Enabled reports whether the environment asks for traces to be exported.
func Enabled() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true") {
		return false
	}
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" ||
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) != ""
}

// Setup installs the OTLP trace exporter when Enabled, with service as the
// service name unless OTEL_SERVICE_NAME names another. It returns the function
// that flushes and stops it; with tracing off that function does nothing.
func Setup(ctx context.Context, service string) (func(context.Context) error, error) {
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", service)),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
	if err != nil && !errors.Is(err, resource.ErrPartialResource) {
		return nil, err
	}
	// The sampler is the SDK's default, which honours OTEL_TRACES_SAMPLER.
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	Install(tp)
	slog.Info("tracing: exporting spans over OTLP/HTTP", "service", service)
	return tp.Shutdown, nil
}

// Install makes tp the process's tracer provider and W3C trace context its
// propagator. Setup calls it; tests call it with an in-memory exporter.
func Install(tp trace.TracerProvider) {
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
}

// Tracer returns REST Mail's tracer from the current provider.
func Tracer() trace.Tracer { return otel.Tracer(Name) }

// Start starts a span named name under ctx's span, tagged with ctx's request ID
// when it has one.
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	ctx, span := Tracer().Start(ctx, name, opts...)
	if id := requestid.FromContext(ctx); id != "" && span.IsRecording() {
		span.SetAttributes(RequestIDKey.String(id))
	}
	return ctx, span
}

// End marks span failed when err is not nil, and ends it. Only the error's Go
// type is kept: an error's text can hold an address, so it never goes on a span.
func End(span trace.Span, err error) {
	Fail(span, err)
	span.End()
}

// Fail marks span failed when err is not nil, keeping only the error's Go type.
func Fail(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.SetAttributes(attribute.String("error.type", fmt.Sprintf("%T", err)))
	span.SetStatus(codes.Error, "")
}

// ShutdownWithin calls shutdown with a deadline, for a binary's exit path.
func ShutdownWithin(shutdown func(context.Context) error, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		slog.Warn("tracing: flushing spans at shutdown failed", "error", err)
	}
}
