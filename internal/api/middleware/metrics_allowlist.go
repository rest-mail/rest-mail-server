package middleware

import (
	"net/http"

	"github.com/restmail/restmail/internal/netallow"
)

// MetricsAllowlistConfig configures the /metrics network gate (OSI-12).
type MetricsAllowlistConfig struct {
	// Path is the exact request path the gate protects (e.g. "/metrics"). The
	// middleware is a transparent pass-through for every other path.
	Path string
	// Allow is the set of source networks permitted to reach the metrics endpoint.
	Allow *netallow.Allowlist
	// TrustedProxies are networks whose X-Forwarded-For is honored when deriving
	// the real client IP. A request whose direct TCP peer is not one of these is
	// judged solely on that peer address, so a forwarded header cannot be spoofed.
	TrustedProxies *netallow.Allowlist
}

// MetricsAllowlist gates the Prometheus /metrics endpoint to an internal-CIDR
// allowlist (OSI-12). Prometheus scraping is a network-level control, so instead
// of JWT-gating (which breaks scrapers) the endpoint is restricted to trusted
// source networks; a non-allowlisted or undeterminable peer gets 404 — the
// endpoint's existence is not advertised to outsiders.
//
// IMPORTANT: install this BEFORE chi's RealIP middleware. RealIP overwrites
// RemoteAddr from the client-controlled X-Forwarded-For / X-Real-IP header, so a
// gate evaluated after it could be bypassed by a public peer spoofing a loopback
// source. Running first, this gate sees the genuine TCP peer and applies its own
// trusted-proxy-aware derivation via netallow.RealClientIP. It is a no-op for any
// path other than cfg.Path.
func MetricsAllowlist(cfg MetricsAllowlistConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != cfg.Path {
				next.ServeHTTP(w, r)
				return
			}
			ip := netallow.RealClientIP(r, cfg.TrustedProxies)
			if !cfg.Allow.Allowed(ip) {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
