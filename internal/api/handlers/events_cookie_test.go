package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
)

// eventRoutes mounts h's stream behind the middleware routes.go puts it behind.
func eventRoutes(h *EventHandler, jwt *auth.JWTService) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.JWTMiddleware(jwt))
	r.Use(middleware.RejectStaleSessions(h.sessionCurrent))
	r.Use(middleware.SetupDone)
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()})
	return r
}

// TestEvents_CookieAuth verifies the SSE endpoint authenticates from the
// restmail_access cookie (what a native browser EventSource sends) — no
// Authorization header, no token in the URL. It is DB-free: the two assertions
// straddle the auth check, which runs before any database access.
func TestEvents_CookieAuth(t *testing.T) {
	jwt := auth.NewJWTService("sse-secret", 15*time.Minute, 7*24*time.Hour)
	routes := eventRoutes(NewEventHandler(nil, NewSSEBroker(), func(uint, int) (bool, error) { return true, nil }), jwt)

	// No credentials at all -> rejected at auth (401), before any DB access.
	t.Run("no credentials is 401", func(t *testing.T) {
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/1/events", nil))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%s)", rr.Code, rr.Body.String())
		}
	})

	// A valid access COOKIE passes authentication: the request goes on to the
	// account id, which 0 is not, so it is refused there (422) rather than at
	// auth (401). Reaching 422 proves the cookie was accepted as credentials.
	t.Run("valid access cookie passes auth", func(t *testing.T) {
		pair, err := jwt.GenerateSession(42, "user@example.test", 0, []string{auth.AMRPassword}, false)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/0/events", nil)
		req.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: pair.AccessToken})
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 after auth (account id 0), got %d (%s)", rr.Code, rr.Body.String())
		}
	})
}
