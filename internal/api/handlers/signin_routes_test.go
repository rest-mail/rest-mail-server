package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
)

// passingSignInChecks stand in for the throttle and the audit log, which have tests of their
// own.
var passingSignInChecks = SessionRouteChecks{
	Throttle: endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }},
	Audit: func(string, ...middleware.AuditOption) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	},
}

// serveAuth serves r, a request to one of the sign-in routes, through their registration.
func serveAuth(h *AuthHandler, w http.ResponseWriter, r *http.Request) {
	routes := chi.NewRouter()
	h.RegisterSignIn(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()}, passingSignInChecks)
	routes.ServeHTTP(w, r)
}

func serveProtocolSignIn(h *CredentialHandler, w http.ResponseWriter, r *http.Request) {
	routes := chi.NewRouter()
	h.RegisterProtocolSignIn(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()}, passingSignInChecks)
	routes.ServeHTTP(w, r)
}

// recoverHandler is recovery through its registration, for a request to any path.
func recoverHandler(h *AuthHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/api/v1/auth/recover"
		serveAuth(h, w, r)
	}
}

// resetHandler is redeeming a reset token through its registration, for a request to any path.
func resetHandler(h *AuthHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/api/v1/auth/reset"
		serveAuth(h, w, r)
	}
}

// ownerRoute is the owner endpoint named, challenge, token or status, through their
// registration, for a request to any path.
func ownerRoute(h *OwnerHandler, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		routes := chi.NewRouter()
		h.Register(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()}, passingSignInChecks.Throttle, passingSignInChecks.Audit)
		r.URL.Path = "/api/v1/auth/owner/" + name
		routes.ServeHTTP(w, r)
	}
}

// authRoute is the sign-in routes through their registration, as a handler.
func authRoute(h *AuthHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { serveAuth(h, w, r) }
}

// The protocol sign-in answer writes the bytes the map it replaced wrote.
func TestProtocolSignedInJSONParity(t *testing.T) {
	user := userInfo{ID: 3, Email: "a@example.test", DisplayName: "A"}
	old, _ := json.Marshal(map[string]any{"user": user, "credential_id": uint(7), "credential_kind": "app_password"})
	current, _ := json.Marshal(ProtocolSignedIn{CredentialID: 7, CredentialKind: "app_password", User: user})
	if string(old) != string(current) {
		t.Fatalf("response JSON changed:\nold %s\nnew %s", old, current)
	}
}

// What binding refuses, it refuses before any account is looked up: a handler with no
// database proves it. A browser's refresh and logout send no body at all.
func TestSignInBindingBeforeDatabase(t *testing.T) {
	h := &AuthHandler{}
	for _, tc := range []struct{ path, body, field string }{
		{"/api/v1/auth/login", `{"email":5,"password":"x"}`, "email"},
		{"/api/v1/auth/login", `{"email":"a@example.test","password":"x","remember":true}`, "remember"},
		{"/api/v1/auth/refresh", `{"refresh_token":12345}`, "refresh_token"},
		{"/api/v1/auth/recover", `{"email":"a@example.test","new_password":12}`, "new_password"},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			serveAuth(h, rr, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"`+tc.field+`":`) {
				t.Fatalf("want 422 for %s, got %d %s", tc.field, rr.Code, rr.Body)
			}
		})
	}
	rr := httptest.NewRecorder()
	serveAuth(h, rr, httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil))
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "No refresh token") {
		t.Fatalf("a refresh with no body and no cookie = %d %s", rr.Code, rr.Body)
	}
	rr = httptest.NewRecorder()
	serveAuth(h, rr, httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil))
	if rr.Code != http.StatusNoContent || len(rr.Header().Values("Set-Cookie")) != 3 {
		t.Fatalf("a logout with no body = %d %v", rr.Code, rr.Header())
	}
	rr = httptest.NewRecorder()
	serveAuth(h, rr, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{}`)))
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "required: the account's address") {
		t.Fatalf("an empty login = %d %s", rr.Code, rr.Body)
	}
}
