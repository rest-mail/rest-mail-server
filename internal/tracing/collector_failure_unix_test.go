//go:build unix

package tracing_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/tracing"
)

const (
	collectorShutdownBudget = 250 * time.Millisecond
	collectorTestDeadline   = 3 * time.Second
)

func TestSetup_CollectorRefusesConnections(t *testing.T) {
	// An established TCP connection reserves its client's ephemeral port
	// without listening for new connections there.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialTimeout("tcp4", listener.Addr().String(), collectorTestDeadline)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	address := client.LocalAddr().String()

	exportErrors := make(chan error, 1)
	previous := otel.GetErrorHandler()
	t.Cleanup(func() { otel.SetErrorHandler(previous) })
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		var opErr *net.OpError
		if !errors.As(err, &opErr) || opErr.Addr == nil || opErr.Addr.String() != address {
			return
		}
		select {
		case exportErrors <- err:
		default:
		}
	}))
	shutdown := setupFailingCollector(t, "http://"+address)
	assertTracedRequestCompletes(t)
	select {
	case err := <-exportErrors:
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("export error = %v, want connection refused", err)
		}
	case <-time.After(collectorTestDeadline):
		t.Fatal("export did not report a refused connection")
	}
	assertTracedRequestCompletes(t)
	_ = assertTracingShutdownCompletes(t, shutdown)
}

func TestSetup_CollectorStalls(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	// Release any blocked export before the provider cleanup runs.
	defer func() {
		close(release)
		collector.Close()
	}()
	shutdown := setupFailingCollector(t, collector.URL)
	assertTracedRequestCompletes(t)
	select {
	case <-started:
	case <-time.After(collectorTestDeadline):
		t.Fatal("collector did not receive the export request")
	}

	// The first export is still waiting for a response while a second request
	// runs and the binary's tracing shutdown deadline elapses.
	assertTracedRequestCompletes(t)
	if err := assertTracingShutdownCompletes(t, shutdown); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown while export is stalled returned %v, want deadline exceeded", err)
	}
}

func setupFailingCollector(t *testing.T, endpoint string) func(context.Context) error {
	t.Helper()
	t.Setenv("OTEL_SDK_DISABLED", "false")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", endpoint+"/v1/traces")
	// Export each request immediately and keep both export timeouts longer
	// than the request and shutdown assertions.
	t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "1")
	t.Setenv("OTEL_BSP_EXPORT_TIMEOUT", "30000")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_TIMEOUT", "30000")
	provider, propagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(provider)
		otel.SetTextMapPropagator(propagator)
	})
	shutdown, err := tracing.Setup(t.Context(), "rest-mail-api")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tracing.ShutdownWithin(shutdown, collectorShutdownBudget) })
	return shutdown
}

func assertTracedRequestCompletes(t *testing.T) {
	t.Helper()
	handler := middleware.Tracing(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !trace.SpanFromContext(r.Context()).IsRecording() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	status := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test", nil))
		status <- response.Code
	}()
	select {
	case got := <-status:
		if got != http.StatusNoContent {
			t.Fatalf("traced request status = %d, want %d", got, http.StatusNoContent)
		}
	case <-time.After(collectorTestDeadline):
		t.Fatal("traced request did not complete while the collector was unavailable")
	}
}

func assertTracingShutdownCompletes(t *testing.T, shutdown func(context.Context) error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		var err error
		tracing.ShutdownWithin(func(ctx context.Context) error {
			err = shutdown(ctx)
			return err
		}, collectorShutdownBudget)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(collectorTestDeadline):
		t.Fatalf("tracing shutdown exceeded %s with a %s budget", collectorTestDeadline, collectorShutdownBudget)
		return nil
	}
}
