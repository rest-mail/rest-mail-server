package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

// TestAuthRateLimit_SpoofedXFFCannotBypass proves an untrusted client cannot
// escape the per-IP auth rate limiter by rotating X-Forwarded-For. The limiter
// keys on the resolved client IP; with no trusted-proxy allowlist configured (the
// default), the forwarded header must be ignored and every request keyed on the
// genuine socket peer, so a single peer spends its burst and is throttled no
// matter what header it sends.
//
// Against a stack that honors X-Forwarded-For unconditionally, each rotated value
// looks like a distinct client with its own fresh token bucket, so the limiter
// never trips — exactly the bypass this test guards against.
func TestAuthRateLimit_SpoofedXFFCannotBypass(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins:   []string{"http://localhost:3000"},
		Environment:          "test",
		AuthRateLimitEnabled: true,
		// Negligible refill so the bucket does not top up mid-test; small burst so
		// the throttle trips quickly once the single real peer is exhausted.
		AuthRateLimitRPS:   0.0001,
		AuthRateLimitBurst: 3,
	}
	router := NewRouter(newFailingGormDB(t), jwtSvc, cfg, nil)

	// One untrusted socket peer, rotating its spoofed X-Forwarded-For each request.
	call := func(spoof string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`))
		req.RemoteAddr = "203.0.113.7:5000"
		req.Header.Set("X-Forwarded-For", spoof)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr.Code
	}

	got429 := false
	for i := 0; i < 12; i++ {
		if call(fmt.Sprintf("10.0.0.%d", i+1)) == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("spoofed X-Forwarded-For bypassed auth rate limiting: a single peer rotating the header was never throttled")
	}
}
