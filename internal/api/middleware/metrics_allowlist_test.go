package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/netallow"
)

// metricsGate builds the gate used across these tests: /metrics restricted to
// loopback + RFC1918, with no trusted proxies (so any X-Forwarded-For is ignored).
func metricsGate() func(http.Handler) http.Handler {
	return MetricsAllowlist(MetricsAllowlistConfig{
		Path:           "/metrics",
		Allow:          netallow.New("test", []string{"127.0.0.0/8", "10.0.0.0/8"}),
		TrustedProxies: netallow.New("test", nil),
	})
}

func serve(h http.Handler, remote, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMetricsAllowlist_AllowlistedPeerReaches(t *testing.T) {
	h := metricsGate()(okHandler)
	if rec := serve(h, "10.1.2.3:44000", "/metrics", nil); rec.Code != http.StatusOK {
		t.Fatalf("allowlisted peer: status %d, want 200", rec.Code)
	}
	if rec := serve(h, "127.0.0.1:5000", "/metrics", nil); rec.Code != http.StatusOK {
		t.Fatalf("loopback peer: status %d, want 200", rec.Code)
	}
}

func TestMetricsAllowlist_PublicPeerDenied(t *testing.T) {
	h := metricsGate()(okHandler)
	if rec := serve(h, "203.0.113.7:5000", "/metrics", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("public peer: status %d, want 404", rec.Code)
	}
}

func TestMetricsAllowlist_UndeterminablePeerDenied(t *testing.T) {
	h := metricsGate()(okHandler)
	if rec := serve(h, "not-an-address", "/metrics", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("undeterminable peer: status %d, want 404", rec.Code)
	}
}

func TestMetricsAllowlist_SpoofedForwardedFromUntrustedPeerDenied(t *testing.T) {
	// A public peer spoofs an internal source via X-Forwarded-For. With no trusted
	// proxies configured, the header must be ignored and the true peer denied.
	h := metricsGate()(okHandler)
	rec := serve(h, "203.0.113.7:5000", "/metrics", map[string]string{"X-Forwarded-For": "127.0.0.1"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("spoofed XFF bypassed gate: status %d, want 404", rec.Code)
	}
}

func TestMetricsAllowlist_NonMetricsPathPassesThrough(t *testing.T) {
	// Any non-/metrics path is untouched regardless of source IP.
	h := metricsGate()(okHandler)
	if rec := serve(h, "203.0.113.7:5000", "/api/health", nil); rec.Code != http.StatusOK {
		t.Fatalf("non-metrics path from public peer: status %d, want 200 (pass-through)", rec.Code)
	}
}

func TestMetricsAllowlist_TrustedProxyForwardedHonored(t *testing.T) {
	// When the direct peer is a trusted proxy, the forwarded origin is evaluated:
	// an allowlisted origin passes, a public origin is denied.
	h := MetricsAllowlist(MetricsAllowlistConfig{
		Path:           "/metrics",
		Allow:          netallow.New("test", []string{"10.0.0.0/8"}),
		TrustedProxies: netallow.New("test", []string{"10.0.0.0/8"}),
	})(okHandler)

	// Proxy peer (10.x, trusted) forwards an allowlisted 10.x origin → allowed.
	if rec := serve(h, "10.0.0.9:5000", "/metrics", map[string]string{"X-Forwarded-For": "10.2.3.4"}); rec.Code != http.StatusOK {
		t.Fatalf("trusted proxy + allowlisted origin: status %d, want 200", rec.Code)
	}
	// Proxy peer forwards a public origin → denied.
	if rec := serve(h, "10.0.0.9:5000", "/metrics", map[string]string{"X-Forwarded-For": "203.0.113.5"}); rec.Code != http.StatusNotFound {
		t.Fatalf("trusted proxy + public origin: status %d, want 404", rec.Code)
	}
}
