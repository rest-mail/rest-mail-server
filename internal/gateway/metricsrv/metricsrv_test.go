package metricsrv

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/metrics"
)

// freePort returns an OS-assigned free TCP port. The listener is closed before
// return, so there is a small reuse window — acceptable for a local test.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestHandlerServesGatewaySeries verifies the gateway /metrics handler serves
// the process's default registry and that the gateway-side series are present
// once incremented — the queue worker's outbound counters and the connection
// limiter's per-protocol gauges/counters.
func TestHandlerServesGatewaySeries(t *testing.T) {
	// Increment the counters/gauges a gateway process touches so their series
	// materialize in the exposition output.
	metrics.MessagesSent.Inc()
	metrics.PipelineTerminal.WithLabelValues("outbound", "delivered").Inc()
	metrics.ActiveConnections.WithLabelValues("smtp").Inc()
	metrics.AuthFailures.WithLabelValues("smtp").Inc()

	srv := httptest.NewServer(Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	got := string(body)

	wantNames := []string{
		"restmail_messages_sent_total",
		"restmail_pipeline_terminal_total",
		"restmail_active_connections",
		"restmail_auth_failures_total",
	}
	for _, name := range wantNames {
		if !strings.Contains(got, name) {
			t.Errorf("metrics output missing series %q", name)
		}
	}
}

// TestNewDisabledPort documents that a non-positive port disables the endpoint:
// New returns nil and the nil-receiver methods are safe no-ops.
func TestNewDisabledPort(t *testing.T) {
	if s := New(0); s != nil {
		t.Fatalf("New(0) = %v, want nil (disabled)", s)
	}
	if s := New(-1); s != nil {
		t.Fatalf("New(-1) = %v, want nil (disabled)", s)
	}

	var s *Server // nil
	s.Start()     // must not panic
	s.Shutdown()  // must not panic
}

// TestServerStartAndShutdown spins the full server on an ephemeral port, scrapes
// it over real HTTP, and shuts it down cleanly.
func TestServerStartAndShutdown(t *testing.T) {
	port := freePort(t)
	s := New(port)
	if s == nil {
		t.Fatalf("New(%d) = nil, want a server", port)
	}
	s.Start()
	defer s.Shutdown()

	// Poll briefly for the listener to come up.
	var resp *http.Response
	var err error
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/metrics"
	for i := 0; i < 50; i++ {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// A non-/metrics path is not served.
	other, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/nope")
	if err != nil {
		t.Fatalf("GET /nope: %v", err)
	}
	defer func() { _ = other.Body.Close() }()
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("GET /nope status = %d, want 404", other.StatusCode)
	}
}
