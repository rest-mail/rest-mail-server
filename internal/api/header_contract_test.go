package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

// Exercise CORS at the actual router, including middleware refusals. A browser
// must be allowed to send the CSRF header together with its session cookies.
func TestBrowserCORSContract(t *testing.T) {
	const allowed = "https://admin.example.test"
	routers := NewRouters(newFailingGormDB(t), auth.NewJWTService(routerTestSecret, time.Minute, time.Hour),
		&config.Config{Environment: "test", CORSAllowedOrigins: []string{allowed}}, nil)
	t.Cleanup(routers.Close)
	for _, origin := range []string{allowed, "https://untrusted.example.test"} {
		for _, target := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/server"},
			{http.MethodGet, "/api/v1/admin/domains"},
			{http.MethodOptions, "/api/v1/admin/domains"},
		} {
			t.Run(origin+target.method+target.path, func(t *testing.T) {
				req := httptest.NewRequest(target.method, target.path, nil)
				req.Header.Set("Origin", origin)
				if target.method == http.MethodOptions {
					req.Header.Set("Access-Control-Request-Method", http.MethodPost)
					req.Header.Set("Access-Control-Request-Headers", "Content-Type, X-CSRF-Token")
				}
				rr := httptest.NewRecorder()
				routers.Public.ServeHTTP(rr, req)
				h := rr.Header()
				if origin != allowed {
					if h.Get("Access-Control-Allow-Origin") != "" || h.Get("Access-Control-Allow-Credentials") != "" {
						t.Fatalf("refused origin was allowed: %v", h)
					}
					return
				}
				if h.Get("Access-Control-Allow-Origin") != allowed || h.Get("Access-Control-Allow-Credentials") != "true" {
					t.Fatalf("credentialed browser request refused: status %d, headers %v", rr.Code, h)
				}
				if target.method == http.MethodOptions && !strings.Contains(strings.ToLower(h.Get("Access-Control-Allow-Headers")), "x-csrf-token") {
					t.Fatalf("preflight does not allow the CSRF header: %v", h)
				}
			})
		}
	}
}

// Every declared 429 must state the retry delay the server sends. This also
// catches new rate-limit responses whose header contract was omitted.
func TestEveryRateLimitDeclaresRetryAfter(t *testing.T) {
	doc := generatedDocument(t)
	paths := doc["paths"].(map[string]interface{})
	for path, item := range paths {
		for method, value := range item.(map[string]interface{}) {
			op := value.(map[string]interface{})
			responses, ok := op["responses"].(map[string]interface{})
			if !ok {
				continue
			}
			limited, ok := responses["429"].(map[string]interface{})
			if !ok {
				continue
			}
			headers, _ := limited["headers"].(map[string]interface{})
			header, _ := headers["Retry-After"].(map[string]interface{})
			schema, _ := header["schema"].(map[string]interface{})
			if header["required"] != true || schema["type"] != "integer" {
				t.Errorf("%s %s must require an integer Retry-After, got %v", method, path, header)
			}
		}
	}
}
