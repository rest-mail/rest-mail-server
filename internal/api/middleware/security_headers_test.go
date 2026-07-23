package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serveWithSecurityHeaders(t *testing.T, cfg SecurityHeadersConfig, path string) http.Header {
	t.Helper()
	handler := SecurityHeaders(cfg)(okHandler)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	return rr.Result().Header
}

func TestSecurityHeaders_PresentOnAPIResponse(t *testing.T) {
	h := serveWithSecurityHeaders(t, SecurityHeadersConfig{HSTSMaxAgeSeconds: 63072000}, "/api/v1/messages/1")

	cases := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for header, want := range cases {
		if got := h.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	if hsts := h.Get("Strict-Transport-Security"); !strings.Contains(hsts, "max-age=63072000") || !strings.Contains(hsts, "includeSubDomains") {
		t.Errorf("Strict-Transport-Security = %q, want max-age + includeSubDomains", hsts)
	}

	csp := h.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("API CSP = %q, want strict default-src/frame-ancestors none", csp)
	}
}

func TestSecurityHeaders_HSTSOmittedWhenZero(t *testing.T) {
	h := serveWithSecurityHeaders(t, SecurityHeadersConfig{HSTSMaxAgeSeconds: 0}, "/api/health")
	if hsts := h.Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("expected no HSTS header when max-age is 0, got %q", hsts)
	}
	// The non-HSTS headers are still present.
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("expected X-Content-Type-Options even when HSTS disabled")
	}
}

func TestSecurityHeaders_DocsGetSwaggerCompatibleCSP(t *testing.T) {
	// The bundled Swagger UI loads its bundle from the unpkg CDN and runs an
	// inline bootstrap; a blanket 'none' CSP would blank it. The docs paths get a
	// relaxed-but-scoped policy that still forbids framing.
	h := serveWithSecurityHeaders(t, SecurityHeadersConfig{HSTSMaxAgeSeconds: 100}, "/api/docs")
	csp := h.Get("Content-Security-Policy")
	if strings.Contains(csp, "default-src 'none'") {
		t.Errorf("docs CSP should not be the strict API policy, got %q", csp)
	}
	if !strings.Contains(csp, "https://unpkg.com") {
		t.Errorf("docs CSP should allow the swagger CDN, got %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("docs CSP should still forbid framing, got %q", csp)
	}
}
