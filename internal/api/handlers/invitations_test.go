package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
)

func invitationRoutes(h *InvitationHandler) http.Handler {
	r := chi.NewRouter()
	h.RegisterPublic(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, passingSignInChecks)
	h.RegisterAdmin(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, MailboxRouteChecks{
		OnMailbox: func(string) endpoint.Middleware { return passingSignInChecks.Throttle },
	})
	return r
}

func TestInvitationRequestBoundaries(t *testing.T) {
	// With no database, every refused case must stop at binding, validation,
	// token presence or enrollment availability instead of touching storage.
	r := invitationRoutes(NewInvitationHandler(nil, nil, false))
	for _, tc := range []struct {
		path, body string
		status     int
		detail     string
	}{
		{"/api/v1/auth/invitation", `{"token":3}`, 422, "token"},
		{"/api/v1/auth/invitation", `{"token":"x","password":"a valid first password","admin":true}`, 422, "admin"},
		{"/api/v1/auth/invitation", `{"token":"x","password":"short"}`, 422, "password"},
		{"/api/v1/auth/invitation", `{"token":"x","password":"a valid first password","client":"other"}`, 422, "client"},
		{"/api/v1/auth/invitation", `{"password":"a valid first password"}`, 401, "invalid_token"},
		{"/api/v1/auth/invitation", `{"token":" ","password":"a valid first password"}`, 401, "invalid_token"},
		{"/api/v1/auth/invitation", `{"token":"x","password":"a valid first password"}`, 503, "unavailable"},
		{"/api/v1/auth/invitation", `{"token":"x","password":"` + strings.Repeat("x", 73) + `"}`, 422, "password"},
		{"/api/v1/auth/invitation", `{"token":"x","password":"` + strings.Repeat("x", 72) + `","client":"native"}`, 503, "unavailable"},
		// The minimum counts characters: six é are 12 bytes and too short,
		// twelve are 24 bytes and long enough.
		{"/api/v1/auth/invitation", `{"token":"x","password":"éééééé"}`, 422, "password"},
		{"/api/v1/auth/invitation", `{"token":"x","password":"éééééééééééé"}`, 503, "unavailable"},
		{"/api/v1/admin/mailboxes/0/invitation", "", 422, "id"},
		{"/api/v1/admin/mailboxes/nope/invitation", "", 422, "id"},
		{"/api/v1/admin/mailboxes/1/invitation", "", 503, "unavailable"},
	} {
		t.Run(tc.path+tc.detail+tc.body, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, withClaims(httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)), freshTwoFactorClaims(1, "admin@example.test")))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.detail) {
				t.Fatalf("got %d %s, want %d with %s", w.Code, w.Body, tc.status, tc.detail)
			}
		})
	}
}

func TestInvitationUsesMailboxWriteAndSignInThrottle(t *testing.T) {
	h := NewInvitationHandler(nil, nil, true)
	r := chi.NewRouter()
	h.RegisterAdmin(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, MailboxRouteChecks{
		OnMailbox: func(capability string) endpoint.Middleware {
			if capability != middleware.CapMailboxesWrite {
				t.Fatalf("wrong authority: %s", capability)
			}
			return endpoint.Middleware{Wrap: func(http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
			}}
		},
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/mailboxes/1/invitation", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("mint bypassed authority: %d", w.Code)
	}
	checks := passingSignInChecks
	checks.Throttle = endpoint.Middleware{Wrap: func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	}}
	r = chi.NewRouter()
	h.RegisterPublic(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, checks)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/invitation", strings.NewReader(`{"token":"x","password":"a valid first password"}`)))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("redemption bypassed throttle: %d", w.Code)
	}
}

func TestInvitationCancellationDoesNotTouchStorage(t *testing.T) {
	r := invitationRoutes(NewInvitationHandler(nil, nil, true))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/invitation", strings.NewReader(`{"token":"not-logged","password":"a valid first password"}`)).WithContext(ctx))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "Failed to redeem the token") || strings.Contains(w.Body.String(), "context canceled") || strings.Contains(w.Body.String(), "not-logged") {
		t.Fatalf("unsafe cancellation response: %d %s", w.Code, w.Body)
	}
	if invitationSecretHeaders().Get("Cache-Control") != "no-store" {
		t.Fatal("invitation secrets are cacheable")
	}
}
