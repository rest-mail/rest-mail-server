package middleware

import (
	"net/http"
	"strconv"
	"strings"
)

// SecurityHeadersConfig configures the SecurityHeaders middleware. Kept as a
// plain struct (not the app Config) so the middleware package stays dependency-
// light, mirroring RateLimitConfig.
type SecurityHeadersConfig struct {
	// HSTSMaxAgeSeconds is the Strict-Transport-Security max-age in seconds.
	// 0 omits the HSTS header entirely (correct for plain-HTTP local dev, where
	// pinning HTTPS would break access).
	HSTSMaxAgeSeconds int
}

const (
	// apiCSP locks a JSON API response all the way down: it loads nothing and
	// cannot be framed. Appropriate because API responses are consumed by
	// fetch/XHR, never rendered as a top-level document with subresources.
	apiCSP = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

	// docsCSP is the relaxed policy for the bundled Swagger UI page, which loads
	// its bundle/styles from the unpkg CDN and runs an inline bootstrap script.
	// A blanket 'none' CSP would render the docs page blank (the exact breakage
	// OSI-11 warns about), so the docs paths get a policy scoped to what Swagger
	// UI needs while still forbidding framing.
	docsCSP = "default-src 'self'; script-src 'self' https://unpkg.com 'unsafe-inline'; " +
		"style-src 'self' https://unpkg.com 'unsafe-inline'; img-src 'self' data:; " +
		"font-src 'self' https://unpkg.com data:; connect-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'"
)

// SecurityHeaders sets standard defense-in-depth response headers on every API
// response (OSI-11): HSTS, X-Content-Type-Options, X-Frame-Options, a
// Referrer-Policy, and a Content-Security-Policy. A reverse proxy is the other
// natural place for these; setting them at the app too keeps responses secure
// even when the proxy is absent or misconfigured, and the headers are
// idempotent so a proxy re-setting them is harmless.
func SecurityHeaders(cfg SecurityHeadersConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			// Stop MIME sniffing turning a JSON/text body into an executable type.
			h.Set("X-Content-Type-Options", "nosniff")
			// Deny framing (clickjacking) — both the legacy header and the CSP
			// directive below, for older and modern agents respectively.
			h.Set("X-Frame-Options", "DENY")
			// Never leak URLs (which can carry ids/tokens) in the Referer header.
			h.Set("Referrer-Policy", "no-referrer")
			if cfg.HSTSMaxAgeSeconds > 0 {
				h.Set("Strict-Transport-Security",
					"max-age="+strconv.Itoa(cfg.HSTSMaxAgeSeconds)+"; includeSubDomains")
			}
			// Strict CSP for the API; a Swagger-UI-compatible policy for the
			// bundled docs so the docs page is not blanked.
			if strings.HasPrefix(r.URL.Path, "/api/docs") {
				h.Set("Content-Security-Policy", docsCSP)
			} else {
				h.Set("Content-Security-Policy", apiCSP)
			}
			next.ServeHTTP(w, r)
		})
	}
}
