package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic limiter tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestLimiter(cfg RateLimitConfig, clk *fakeClock) *rateLimiter {
	rl := newRateLimiter(cfg)
	rl.now = clk.now
	return rl
}

func TestRateLimiter_BurstThenThrottle(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	rl := newTestLimiter(RateLimitConfig{RPS: 1, Burst: 3}, clk)

	// A full bucket permits exactly Burst requests at the same instant.
	for i := 0; i < 3; i++ {
		if ok, _ := rl.allow("10.0.0.1"); !ok {
			t.Fatalf("request %d within burst should be allowed", i+1)
		}
	}
	// The next request in the same instant is throttled with a positive wait.
	ok, wait := rl.allow("10.0.0.1")
	if ok {
		t.Fatal("request beyond burst should be throttled")
	}
	if wait <= 0 {
		t.Fatalf("throttled request should report a positive Retry-After wait, got %v", wait)
	}
}

func TestRateLimiter_RefillOverTime(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	rl := newTestLimiter(RateLimitConfig{RPS: 1, Burst: 2}, clk)

	// Drain the bucket.
	rl.allow("10.0.0.2")
	rl.allow("10.0.0.2")
	if ok, _ := rl.allow("10.0.0.2"); ok {
		t.Fatal("bucket should be empty after draining the burst")
	}

	// At RPS=1, one second restores exactly one token.
	clk.advance(time.Second)
	if ok, _ := rl.allow("10.0.0.2"); !ok {
		t.Fatal("one token should have refilled after 1s at RPS=1")
	}
	// ...but not two.
	if ok, _ := rl.allow("10.0.0.2"); ok {
		t.Fatal("only one token should refill per second at RPS=1")
	}
}

func TestRateLimiter_PerKeyIsolation(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	rl := newTestLimiter(RateLimitConfig{RPS: 1, Burst: 1}, clk)

	if ok, _ := rl.allow("10.0.0.3"); !ok {
		t.Fatal("first client should be allowed")
	}
	if ok, _ := rl.allow("10.0.0.3"); ok {
		t.Fatal("first client should now be throttled")
	}
	// A different client has its own independent bucket.
	if ok, _ := rl.allow("10.0.0.4"); !ok {
		t.Fatal("second client must not be affected by the first client's bucket")
	}
}

func TestRateLimiter_IdleBucketsSwept(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	rl := newTestLimiter(RateLimitConfig{RPS: 1, Burst: 1, TTL: time.Minute}, clk)

	rl.allow("10.0.0.5")
	if got := len(rl.buckets); got != 1 {
		t.Fatalf("expected 1 tracked bucket, got %d", got)
	}

	// Advance past the TTL and touch a different key; the idle bucket is swept.
	clk.advance(2 * time.Minute)
	rl.allow("10.0.0.6")
	if _, present := rl.buckets["10.0.0.5"]; present {
		t.Fatal("idle bucket should have been swept after TTL")
	}
}

func TestRateLimit_Middleware_ThrottlesPerIP(t *testing.T) {
	mw := RateLimit(RateLimitConfig{RPS: 1, Burst: 2})
	handler := mw(okHandler)

	call := func(remoteAddr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		req.RemoteAddr = remoteAddr
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	// Burst of 2 allowed, third throttled — all from the same IP.
	if rr := call("203.0.113.5:5001"); rr.Code != http.StatusOK {
		t.Fatalf("request 1: expected 200, got %d", rr.Code)
	}
	if rr := call("203.0.113.5:5002"); rr.Code != http.StatusOK {
		t.Fatalf("request 2: expected 200, got %d", rr.Code)
	}
	rr := call("203.0.113.5:5003")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("request 3: expected 429, got %d", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("429 response must carry a Retry-After header")
	}
	if code := parseErrorResponse(t, rr).Error.Code; code != "rate_limited" {
		t.Errorf("expected error code %q, got %q", "rate_limited", code)
	}

	// A different IP is unaffected by the first IP's exhausted bucket.
	if rr := call("198.51.100.9:6000"); rr.Code != http.StatusOK {
		t.Fatalf("distinct IP should not be throttled, got %d", rr.Code)
	}
}
