package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/limittest"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
)

// Limits from #660's table that the limits suite leaves unproven (#1166), held
// here through the production router and its real middleware. Every limit below
// answers before the handler reaches the database, so the deliberately failing
// database of routes_test.go is enough.

// throttledRoute is a route behind one of the per-IP throttles.
type throttledRoute struct {
	name          string
	method, path  string
	adminOnly     bool
	configureWith func(*config.Config)
}

var perIPThrottles = []throttledRoute{
	{name: "authThrottle", method: http.MethodPost, path: "/api/v1/auth/login",
		configureWith: func(c *config.Config) {
			c.AuthRateLimitEnabled, c.AuthRateLimitRPS, c.AuthRateLimitBurst = true, 0.0001, 3
		}},
	{name: "ownerThrottle", method: http.MethodGet, path: "/api/v1/server",
		configureWith: func(c *config.Config) {
			c.AuthRateLimitEnabled, c.AuthRateLimitRPS, c.AuthRateLimitBurst = true, 0.0001, 3
		}},
	{name: "tlsrptThrottle", method: http.MethodPost, path: "/.well-known/smtp-tlsrpt",
		configureWith: func(c *config.Config) {
			c.TLSRPTRateLimitEnabled, c.TLSRPTRateLimitRPS, c.TLSRPTRateLimitBurst = true, 0.0001, 3
		}},
	{name: "pipelineTestThrottle", method: http.MethodPost, path: "/api/v1/admin/pipelines/test", adminOnly: true,
		configureWith: func(c *config.Config) {
			c.PipelineTestRateLimitEnabled, c.PipelineTestRateLimitRPS, c.PipelineTestRateLimitBurst = true, 0.0001, 3
		}},
}

// requestFrom sends route's request as the socket peer remoteAddr, with an
// X-Forwarded-For when forwarded is set.
func requestFrom(router http.Handler, route throttledRoute, token, remoteAddr, forwarded string) *httptest.ResponseRecorder {
	var body *bytes.Reader
	if route.method == http.MethodPost {
		body = bytes.NewReader([]byte(`{}`))
	} else {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(route.method, route.path, body)
	if route.method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = remoteAddr
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// exhaust sends from one client until the throttle answers 429, and fails t if
// it never does within a generous number of requests.
func exhaust(t *testing.T, send func() *httptest.ResponseRecorder) {
	t.Helper()
	for i := 0; i < 20; i++ {
		if rr := send(); rr.Code == http.StatusTooManyRequests {
			limittest.RetryAfter(t, rr.Header())
			return
		}
	}
	t.Fatal("the throttle never answered 429")
}

// Each per-IP throttle keys on the client IP the router resolves: the socket
// peer, or the X-Forwarded-For a trusted proxy sends. Once one client has spent
// its burst, a different client is not throttled, and a client cannot borrow
// another's bucket by sending a header the router does not trust.
func TestPerIPThrottles_AnotherClientIPIsUnaffected(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	token := adminToken(t, jwtSvc, []string{"*"})

	for _, route := range perIPThrottles {
		t.Run(route.name+"/direct", func(t *testing.T) {
			cfg := &config.Config{Environment: "test"}
			route.configureWith(cfg)
			routers := NewRouters(newFailingGormDB(t), jwtSvc, cfg, nil)
			t.Cleanup(routers.Close)
			bearer := ""
			if route.adminOnly {
				bearer = token
			}

			exhaust(t, func() *httptest.ResponseRecorder {
				return requestFrom(routers.Public, route, bearer, "203.0.113.7:5000", "")
			})
			// The spent client naming another address in a header nobody trusts
			// is still the spent client.
			if rr := requestFrom(routers.Public, route, bearer, "203.0.113.7:5001", "198.51.100.9"); rr.Code != http.StatusTooManyRequests {
				t.Fatalf("the spent client with an untrusted X-Forwarded-For: got %d, want 429", rr.Code)
			}
			rr := requestFrom(routers.Public, route, bearer, "198.51.100.9:5000", "")
			if rr.Code == http.StatusTooManyRequests {
				t.Fatalf("another client IP was throttled by the first one's spent burst: %s", rr.Body.String())
			}
		})

		t.Run(route.name+"/through a trusted proxy", func(t *testing.T) {
			cfg := &config.Config{Environment: "test", ProxyProtocolTrustedCIDRs: []string{"10.0.0.0/8"}}
			route.configureWith(cfg)
			routers := NewRouters(newFailingGormDB(t), jwtSvc, cfg, nil)
			t.Cleanup(routers.Close)
			bearer := ""
			if route.adminOnly {
				bearer = token
			}

			exhaust(t, func() *httptest.ResponseRecorder {
				return requestFrom(routers.Public, route, bearer, "10.0.0.5:40000", "203.0.113.7")
			})
			rr := requestFrom(routers.Public, route, bearer, "10.0.0.5:40001", "198.51.100.9")
			if rr.Code == http.StatusTooManyRequests {
				t.Fatalf("another client behind the trusted proxy was throttled by the first one's spent burst: %s", rr.Body.String())
			}
		})
	}
}

// cappedRoute is an operation behind a request body cap, with the cap the
// production router gives it under bodyCapConfig.
type cappedRoute struct {
	name, path string
	token      string
	cap        int64
}

// bodyCapConfig sets the API body cap low, so a body one byte over it is small.
var bodyCapConfig = config.Config{Environment: "test", APIMaxBodyBytes: 1024}

// postUndeclared POSTs size bytes of JSON whitespace with no declared length
// (sent chunked). Whitespace makes a JSON decoder read the whole body before it
// fails, so a body over the cap always reaches the cap's reader.
func postUndeclared(router http.Handler, route cappedRoute, size int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, route.path, bytes.NewReader(bytes.Repeat([]byte(" "), int(size))))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	if route.token != "" {
		req.Header.Set("Authorization", "Bearer "+route.token)
	}
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// Every capped operation other than delivery (#1395, #1689), with the length not
// declared: a body exactly at the cap is not refused for its size, and one byte
// over is the 413 with the error envelope, counted on the bytes read.
func TestBodyCaps_UndeclaredLengthIsCountedOnEveryCappedOperation(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := bodyCapConfig
	routers := NewRouters(newFailingGormDB(t), jwtSvc, &cfg, nil)
	t.Cleanup(routers.Close)
	admin := adminToken(t, jwtSvc, []string{"*"})
	mailbox := mailboxToken(t, jwtSvc)

	for _, route := range []cappedRoute{
		{name: "sign-in (publicAuth)", path: "/api/v1/auth/login", cap: cfg.APIMaxBodyBytes},
		{name: "invitation redemption", path: "/api/v1/auth/invitation", cap: cfg.APIMaxBodyBytes},
		{name: "account creation", path: "/api/v1/accounts", token: admin, cap: cfg.APIMaxBodyBytes},
		{name: "own two-factor (person session)", path: "/api/v1/auth/2fa/confirm", token: mailbox, cap: cfg.APIMaxBodyBytes},
		{name: "send (mailbox surface)", path: "/api/v1/messages/send", token: mailbox, cap: cfg.APIMaxBodyBytes},
		{name: "pipeline creation (admin surface)", path: "/api/v1/admin/pipelines", token: admin, cap: cfg.APIMaxBodyBytes},
		{name: "autodiscover", path: "/autodiscover/autodiscover.xml", cap: 64 << 10},
		{name: "TLS-RPT ingestion", path: "/.well-known/smtp-tlsrpt", cap: 512 << 10},
	} {
		t.Run(route.name, func(t *testing.T) {
			if rr := postUndeclared(routers.Public, route, route.cap); refusedForBodySize(rr) {
				t.Errorf("a body at the cap was refused for size: %d %s", rr.Code, rr.Body.String())
			}
			rr := postUndeclared(routers.Public, route, route.cap+1)
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("one byte over the cap, length not declared: got %d, want 413: %s", rr.Code, rr.Body.String())
			}
			var refusal bodyCapRefusal
			if err := json.Unmarshal(rr.Body.Bytes(), &refusal); err != nil || refusal.Error.Code != "request_too_large" {
				t.Errorf("the 413 is not the request_too_large envelope: %s", rr.Body.String())
			}
		})
	}
}

type bodyCapRefusal struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func refusedForBodySize(rr *httptest.ResponseRecorder) bool {
	var refusal bodyCapRefusal
	_ = json.Unmarshal(rr.Body.Bytes(), &refusal)
	return rr.Code == http.StatusRequestEntityTooLarge || refusal.Error.Code == "request_too_large"
}
