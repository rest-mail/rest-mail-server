package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

// apiKeyRefusalLimited answers a request the way a spent refusal budget does (#1269). Its
// server holds one refused API key per address, and every request is sent twice with a
// malformed key, so the second answers 429 rate_limited from the API-key middleware before
// any check behind it, or the handler, runs. That is what an admin operation's
// rate_limited example shows.
func apiKeyRefusalLimited(t *testing.T) http.Handler {
	t.Helper()
	jwt := auth.NewJWTService(routerTestSecret, 15*time.Minute, time.Hour)
	r := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8,
		AuthRateLimitEnabled: true, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
	t.Cleanup(r.Close)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.Header.Set("Authorization", "Bearer rmk_invalid")
		r.Public.ServeHTTP(httptest.NewRecorder(), req.Clone(req.Context()))
		r.Public.ServeHTTP(w, req)
	})
}

func TestAPIKeyRefusalLimitedAnswersTheThrottle(t *testing.T) {
	rr := httptest.NewRecorder()
	apiKeyRefusalLimited(t).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/domains", nil))
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("got %d %q, want 429 with Retry-After", rr.Code, rr.Header().Get("Retry-After"))
	}
}
