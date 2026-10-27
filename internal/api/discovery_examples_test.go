package api

import (
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

// The 429 pair has a stateful precondition: the same client has already used
// the shared owner-endpoint budget. Exercise the actual router and limiter,
// without credentials, sleeping for a refill or replacing the middleware.
func TestDiscoveryRateLimitExampleMatchesRegisteredResponse(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	pair, ok := pairs["describeServer"]["rate_limited"]
	if !ok {
		t.Fatal("the named rate-limit pair is missing")
	}
	jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{Environment: "test", AuthRateLimitEnabled: true, AuthRateLimitRPS: 0.0001, AuthRateLimitBurst: 1}
	routers := NewRouters(newFailingGormDB(t), jwt, cfg, nil)
	t.Cleanup(routers.Close)
	for attempt := 0; attempt < 2; attempt++ {
		rr := httptest.NewRecorder()
		routers.Public.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/server", nil))
		if attempt == 0 {
			if rr.Code != http.StatusOK {
				t.Fatalf("the first discovery request was not admitted: %d %s", rr.Code, rr.Body.String())
			}
			continue
		}
		if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
			t.Fatalf("got %d %v", rr.Code, rr.Header())
		}
		retry, err := strconv.Atoi(rr.Header().Get("Retry-After"))
		if err != nil || retry < 1 {
			t.Fatalf("429 has no positive Retry-After: %v", rr.Header())
		}
		assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
	}
}
