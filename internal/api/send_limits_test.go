package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

// These tests cover issue #184: the authenticated API surface must cap request
// bodies (memory-exhaustion), and the send path must cap recipients-per-message
// and rate-limit per account so a single compromised webmail credential cannot
// fan out unlimited bulk mail. They run against the routes_test.go harness (a
// deliberately-failing DB): every limit here is enforced BEFORE any DB access,
// so a limit rejection is proof the guard fired, and a request that instead
// reaches the DB-dependent handler logic (non-limit status) proves it did not.

// errorCode decodes the standard API error body and returns its machine code.
func errorCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var resp middleware.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		return ""
	}
	return resp.Error.Code
}

// ── (a) Authenticated request-body size cap ───────────────────────────

// TestAuthenticatedRoutes_BodySizeLimited proves an oversized request body on an
// authenticated route is rejected with 413 before it is buffered/decoded. Before
// the fix the authenticated group had no body cap, so the oversized body was
// decoded and the request fell through to the handler.
func TestAuthenticatedRoutes_BodySizeLimited(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins: []string{"http://localhost:3000"},
		Environment:        "test",
		APIMaxBodyBytes:    1024, // 1 KiB cap
	}
	router := NewRouter(newFailingGormDB(t), jwtSvc, cfg, nil)
	token := mailboxToken(t, jwtSvc)

	big := `{"from":"a@x.test","to":["b@y.test"],"subject":"x","body_text":"` +
		strings.Repeat("x", 8192) + `"}`
	rr := doRequest(router, http.MethodPost, "/api/v1/messages/send", token, big)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: expected 413, got %d (body %s)", rr.Code, rr.Body.String())
	}
}

// TestAuthenticatedRoutes_BodySizeDisabled is the regression guard: with the cap
// disabled (0), a large body is not rejected for size — it reaches the handler.
func TestAuthenticatedRoutes_BodySizeDisabled(t *testing.T) {
	router, jwtSvc := newTestRouter(t) // APIMaxBodyBytes defaults to 0 (disabled)
	token := mailboxToken(t, jwtSvc)

	big := `{"from":"a@x.test","to":["b@y.test"],"subject":"x","body_text":"` +
		strings.Repeat("x", 8192) + `"}`
	rr := doRequest(router, http.MethodPost, "/api/v1/messages/send", token, big)
	if rr.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("body cap disabled but request got 413")
	}
	assertReachedHandler(t, rr)
}

// ── (b) Per-message recipient cap ─────────────────────────────────────

// TestSendMessage_RecipientCapEnforced proves a message whose recipient count
// (to+cc+bcc) exceeds the cap is rejected with 400/too_many_recipients before
// anything is queued. Before the fix there was no recipient cap.
func TestSendMessage_RecipientCapEnforced(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins:         []string{"http://localhost:3000"},
		Environment:                "test",
		APIMaxRecipientsPerMessage: 3,
	}
	router := NewRouter(newFailingGormDB(t), jwtSvc, cfg, nil)
	token := mailboxToken(t, jwtSvc)

	// 5 recipients across to/cc/bcc exceeds the cap of 3.
	body := `{"from":"a@x.test","to":["b1@y.test","b2@y.test","b3@y.test"],` +
		`"cc":["c1@y.test"],"bcc":["d1@y.test"],"subject":"x","body_text":"hi"}`
	rr := doRequest(router, http.MethodPost, "/api/v1/messages/send", token, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("too many recipients: expected 400, got %d (body %s)", rr.Code, rr.Body.String())
	}
	if code := errorCode(t, rr); code != "too_many_recipients" {
		t.Fatalf("expected error code too_many_recipients, got %q (body %s)", code, rr.Body.String())
	}
}

// TestSendMessage_RecipientCapDisabled is the regression guard: with the cap
// disabled (0), a many-recipient message is not rejected for recipient count.
func TestSendMessage_RecipientCapDisabled(t *testing.T) {
	router, jwtSvc := newTestRouter(t) // APIMaxRecipientsPerMessage defaults to 0 (disabled)
	token := mailboxToken(t, jwtSvc)

	body := `{"from":"a@x.test","to":["b1@y.test","b2@y.test","b3@y.test","b4@y.test","b5@y.test"],` +
		`"subject":"x","body_text":"hi"}`
	rr := doRequest(router, http.MethodPost, "/api/v1/messages/send", token, body)
	if rr.Code == http.StatusBadRequest && errorCode(t, rr) == "too_many_recipients" {
		t.Fatalf("recipient cap disabled but request rejected for too many recipients")
	}
	assertReachedHandler(t, rr)
}

// ── (b) Per-account send rate limit ───────────────────────────────────

// TestSendMessage_RateLimited proves the send path is rate limited per account:
// once the per-minute cap is spent, further sends get 429 with a Retry-After
// header. Before the fix the API send path had no handler-level throttle.
func TestSendMessage_RateLimited(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins:        []string{"http://localhost:3000"},
		Environment:               "test",
		APISendRateLimitPerMinute: 2,
		APISendRateLimitPerHour:   100,
	}
	router := NewRouter(newFailingGormDB(t), jwtSvc, cfg, nil)
	token := mailboxToken(t, jwtSvc)
	body := `{"from":"a@x.test","to":["b@y.test"],"subject":"x","body_text":"hi"}`

	passes := 0
	got429 := false
	for i := 0; i < 8; i++ {
		rr := doRequest(router, http.MethodPost, "/api/v1/messages/send", token, body)
		if rr.Code == http.StatusTooManyRequests {
			got429 = true
			if rr.Header().Get("Retry-After") == "" {
				t.Errorf("429 response missing Retry-After header")
			}
			break
		}
		// A non-429 means the throttle admitted the request to the handler (which
		// then errors on the failing DB / handler-level 403).
		passes++
	}
	if !got429 {
		t.Fatalf("expected a 429 after exhausting the per-minute send cap, never got one")
	}
	if passes < cfg.APISendRateLimitPerMinute {
		t.Errorf("throttle tripped too early: %d passes before 429, want >= cap (%d)", passes, cfg.APISendRateLimitPerMinute)
	}
}

// TestSendMessage_RateLimitDisabled is the regression guard: with both send-rate
// tiers disabled (0), the send path never 429s no matter how hard it is hit.
func TestSendMessage_RateLimitDisabled(t *testing.T) {
	router, jwtSvc := newTestRouter(t) // send-rate tiers default to 0 (disabled)
	token := mailboxToken(t, jwtSvc)
	body := `{"from":"a@x.test","to":["b@y.test"],"subject":"x","body_text":"hi"}`

	for i := 0; i < 30; i++ {
		rr := doRequest(router, http.MethodPost, "/api/v1/messages/send", token, body)
		if rr.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d got 429 with the send rate limit disabled", i)
		}
	}
}
