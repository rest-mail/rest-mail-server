package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
)

// TestEvents_CookieAuth verifies the SSE endpoint authenticates from the
// restmail_access cookie (what a native browser EventSource sends) — no
// Authorization header, no token in the URL. It is DB-free: the two assertions
// straddle the auth check, which runs before any database access.
func TestEvents_CookieAuth(t *testing.T) {
	jwt := auth.NewJWTService("sse-secret", 15*time.Minute, 7*24*time.Hour)
	h := NewEventHandler(nil, NewSSEBroker(), jwt)

	// No credentials at all -> rejected at auth (401), before any DB access.
	t.Run("no credentials is 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/1/events", nil)
		rr := httptest.NewRecorder()
		h.Events(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%s)", rr.Code, rr.Body.String())
		}
	})

	// A valid access COOKIE passes authentication: the handler proceeds past the
	// auth gate to parse the account id (which is absent here, since no chi route
	// context is attached), yielding 400 rather than 401. Reaching 400 proves the
	// cookie was accepted as credentials.
	t.Run("valid access cookie passes auth", func(t *testing.T) {
		pair, err := jwt.GenerateTokenPair(42, "user@example.test", 7)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/1/events", nil)
		req.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: pair.AccessToken})
		rr := httptest.NewRecorder()
		h.Events(rr, req)
		if rr.Code == http.StatusUnauthorized {
			t.Fatalf("cookie auth should have passed, got 401 (%s)", rr.Body.String())
		}
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 after auth (missing account id), got %d (%s)", rr.Code, rr.Body.String())
		}
	})
}
