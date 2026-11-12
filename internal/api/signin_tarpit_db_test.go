package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
)

// Through the real router and on the real clock: the wait is wired up, gateway
// and sign-in share one count, nobody is refused, and the correct password does
// not wait however high the count has got (#1643).
func TestSignInFailuresWaitThroughTheRouter(t *testing.T) {
	f := newCredentialFixtureWith(t, &config.Config{
		Environment:          "test",
		AuthRateLimitEnabled: true,
		AuthRateLimitRPS:     100,
		AuthRateLimitBurst:   100,
		InternalMTLSEnabled:  true,
	})

	attempt := func(ip, path, body string, gateway bool) (int, time.Duration) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = ip + ":40000"
		handler := f.router
		if gateway {
			handler = asGateway(imapCN, f.routers.Internal)
		}
		rr := httptest.NewRecorder()
		start := time.Now()
		handler.ServeHTTP(rr, req)
		return rr.Code, time.Since(start)
	}
	login := func(ip, password string) (int, time.Duration) {
		t.Helper()
		return attempt(ip, "/api/v1/auth/login", fmt.Sprintf(`{"email":%q,"password":%q,"client":"native"}`, f.address, password), false)
	}
	protocolSignIn := func(ip, password string) (int, time.Duration) {
		t.Helper()
		return attempt(ip, "/api/v1/gateway-sessions", fmt.Sprintf(`{"protocol":"imap","address":%q,"password":%q}`, f.address, password), true)
	}

	// Five free failures, half of them over the gateway, each from its own address.
	for i := 1; i <= 5; i++ {
		ip, code := fmt.Sprintf("198.51.100.%d", i), 0
		if i%2 == 0 {
			code, _ = protocolSignIn(ip, "a wrong guess")
		} else {
			code, _ = login(ip, "a wrong guess")
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d", i, code)
		}
	}
	const margin = 50 * time.Millisecond
	if code, elapsed := login("198.51.100.6", "a wrong guess"); code != http.StatusUnauthorized || elapsed < 250*time.Millisecond-margin {
		t.Errorf("sixth failure: %d after %v, want 401 after at least 250ms", code, elapsed)
	}
	if code, elapsed := protocolSignIn("198.51.100.7", "a wrong guess"); code != http.StatusUnauthorized || elapsed < 500*time.Millisecond-margin {
		t.Errorf("seventh failure, over the gateway: %d after %v, want 401 after at least 500ms", code, elapsed)
	}
	if code, elapsed := login("198.51.100.8", "a wrong guess"); code != http.StatusUnauthorized || elapsed < time.Second-margin {
		t.Errorf("eighth failure: %d after %v, want 401 after at least 1s", code, elapsed)
	}

	// The next failure here would wait 2s, so these are far below what a wait
	// would take, however slow the machine's bcrypt.
	if code, elapsed := login("192.0.2.77", credentialTestPassword); code != http.StatusOK || elapsed > 1500*time.Millisecond {
		t.Errorf("the correct password during the attack: %d after %v, want 200 with no wait", code, elapsed)
	}
	if code, elapsed := protocolSignIn("198.51.100.1", credentialTestPassword); code != http.StatusCreated || elapsed > 1500*time.Millisecond {
		t.Errorf("the correct password over the gateway, from an attacker's address: %d after %v, want 201 with no wait", code, elapsed)
	}
}
