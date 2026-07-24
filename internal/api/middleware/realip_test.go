package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/netallow"
)

// resolvedRemoteAddr serves a request with the given peer and headers through the
// TrustedRealIP middleware and returns the RemoteAddr the middleware settled on.
// The middleware mutates r.RemoteAddr in place, so reading it back after the
// (no-op) handler runs reflects the resolution.
func resolvedRemoteAddr(trusted *netallow.Allowlist, remote string, headers map[string]string) string {
	h := TrustedRealIP(trusted)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return req.RemoteAddr
}

func TestTrustedRealIP_UntrustedPeerIgnoresForwardedHeader(t *testing.T) {
	// No trusted proxies configured (the default): a forwarded header from a public
	// peer must be ignored and RemoteAddr resolved to the genuine socket peer.
	got := resolvedRemoteAddr(netallow.New("test", nil), "203.0.113.7:5000",
		map[string]string{"X-Forwarded-For": "10.0.0.1"})
	if got != "203.0.113.7" {
		t.Fatalf("untrusted peer: RemoteAddr = %q, want the genuine socket peer 203.0.113.7 (forwarded header ignored)", got)
	}
}

func TestTrustedRealIP_TrustedProxyUsesRightmostUntrustedHop(t *testing.T) {
	// The direct peer 10.0.0.9 is a trusted proxy, so the real origin is taken as
	// the rightmost X-Forwarded-For value that is not itself a trusted proxy.
	got := resolvedRemoteAddr(netallow.New("test", []string{"10.0.0.0/8"}), "10.0.0.9:5000",
		map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.9"})
	if got != "203.0.113.9" {
		t.Fatalf("trusted proxy: RemoteAddr = %q, want the real origin 203.0.113.9", got)
	}
}
