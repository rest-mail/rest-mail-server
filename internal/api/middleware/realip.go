package middleware

import (
	"net/http"

	"github.com/restmail/restmail/internal/netallow"
)

// TrustedRealIP derives the genuine client IP and writes it back into
// r.RemoteAddr, but honors the forwarding headers ONLY when the direct TCP peer
// is inside the configured trusted-proxy allowlist. When the peer is untrusted —
// the default, since the allowlist is empty unless PROXY_PROTOCOL_TRUSTED_CIDRS
// is set — the raw socket peer is used and any X-Forwarded-For is ignored, so a
// public client cannot spoof its source address with a forged header. When the
// peer IS a trusted proxy, the real origin is taken as the rightmost X-Forwarded-
// For value that is not itself a trusted proxy (see netallow.RealClientIP).
//
// This is the trusted-proxy-aware replacement for chi's RealIP middleware, which
// rewrites RemoteAddr from those headers unconditionally. Every downstream control
// that keys on the client IP — the auth login/refresh rate limiter, the RESTMAIL
// delivery-auth trusted-CIDR gate, and the negative-lookup tarpit — reads the
// resolved RemoteAddr, so gating the rewrite here closes the spoofing vector for
// all of them at once. An undeterminable peer leaves RemoteAddr untouched.
func TrustedRealIP(trustedProxies *netallow.Allowlist) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ip := netallow.RealClientIP(r, trustedProxies); ip != nil {
				r.RemoteAddr = ip.String()
			}
			next.ServeHTTP(w, r)
		})
	}
}
