package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
)

// mintAccessCookie returns a valid restmail_access cookie for a fresh mailbox
// access token, so the cookie-auth and CSRF paths can be exercised with a token
// the middleware will accept.
func mintAccessCookie(t *testing.T, jwtSvc *auth.JWTService) *http.Cookie {
	t.Helper()
	pair, err := jwtSvc.GenerateTokenPair(42, "user@example.com", 7)
	if err != nil {
		t.Fatalf("generate token pair: %v", err)
	}
	return &http.Cookie{Name: auth.AccessCookieName, Value: pair.AccessToken}
}

// TestJWTMiddleware_CookieAuth: an access token presented via the
// restmail_access cookie (no Authorization header) authenticates the request —
// the browser SPA transport.
func TestJWTMiddleware_CookieAuth(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)

	var captured *auth.Claims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = GetClaims(r)
		w.WriteHeader(http.StatusOK)
	})
	handler := JWTMiddleware(jwtSvc)(inner)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(mintAccessCookie(t, jwtSvc))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("cookie auth: expected 200, got %d", rr.Code)
	}
	if captured == nil || captured.MailboxID != 42 {
		t.Fatalf("cookie auth did not populate claims: %+v", captured)
	}
}

// TestJWTMiddleware_NoCookieNoHeader: neither a cookie nor a header is 401.
func TestJWTMiddleware_NoCookieNoHeader(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	handler := JWTMiddleware(jwtSvc)(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: expected 401, got %d", rr.Code)
	}
}

// csrfHandler wraps okHandler in the CSRF guard for the tests below.
func csrfHandler() http.Handler { return CSRF()(okHandler) }

// TestCSRF_SafeMethodExempt: a GET is never CSRF-checked, even carrying a
// session cookie and no token.
func TestCSRF_SafeMethodExempt(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/messages/1", nil)
	req.AddCookie(mintAccessCookie(t, jwtSvc))
	rr := httptest.NewRecorder()
	csrfHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("safe GET: expected 200, got %d", rr.Code)
	}
}

// TestCSRF_BearerOnlyExempt: a mutating request with NO session cookie (a
// Bearer-token gateway call) is exempt — it is not CSRF-attackable.
func TestCSRF_BearerOnlyExempt(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", nil)
	req.Header.Set("Authorization", "Bearer some.jwt.token")
	rr := httptest.NewRecorder()
	csrfHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("bearer-only POST: expected 200 (exempt), got %d", rr.Code)
	}
}

// TestCSRF_CookieSessionMissingHeaderRejected: a cookie-authenticated mutation
// with no X-CSRF-Token header is 403.
func TestCSRF_CookieSessionMissingHeaderRejected(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", nil)
	req.AddCookie(mintAccessCookie(t, jwtSvc))
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "csrf-abc"})
	// No X-CSRF-Token header.
	rr := httptest.NewRecorder()
	csrfHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF header: expected 403, got %d", rr.Code)
	}
}

// TestCSRF_CookieSessionMismatchedHeaderRejected: header present but not equal
// to the cookie is 403.
func TestCSRF_CookieSessionMismatchedHeaderRejected(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/messages/1", nil)
	req.AddCookie(mintAccessCookie(t, jwtSvc))
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "csrf-abc"})
	req.Header.Set(auth.CSRFHeaderName, "csrf-WRONG")
	rr := httptest.NewRecorder()
	csrfHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("mismatched CSRF header: expected 403, got %d", rr.Code)
	}
}

// TestCSRF_CookieSessionMatchingHeaderAllowed: matching double-submit passes.
func TestCSRF_CookieSessionMatchingHeaderAllowed(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", nil)
	req.AddCookie(mintAccessCookie(t, jwtSvc))
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "csrf-match"})
	req.Header.Set(auth.CSRFHeaderName, "csrf-match")
	rr := httptest.NewRecorder()
	csrfHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("matching CSRF header: expected 200, got %d", rr.Code)
	}
}
