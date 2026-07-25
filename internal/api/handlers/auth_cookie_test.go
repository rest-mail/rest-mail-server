package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
)

// cookieByName returns the named cookie set on a response, or nil.
func cookieByName(rr *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestRefresh_DeliversAccessTokenAsHttpOnlyCookieNotBody proves the core #194
// hardening on the token-minting path (exercised DB-less via Refresh, which sets
// the identical session cookies as login): the access token is delivered as a
// HttpOnly + Secure + SameSite=Strict cookie and is NOT present in the JSON body,
// so page JavaScript cannot read it; a readable CSRF companion cookie is set
// alongside.
func TestRefresh_DeliversAccessTokenAsHttpOnlyCookieNotBody(t *testing.T) {
	jwt := auth.NewJWTService("cookie-secret", 15*time.Minute, 7*24*time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)

	pair, err := jwt.GenerateTokenPair(42, "user@example.test", 3)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, "mailbox", 42)

	rr := doRefresh(h, pair.RefreshToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	// Access token: present as a cookie, with the hardening attributes.
	access := cookieByName(rr, auth.AccessCookieName)
	if access == nil || access.Value == "" {
		t.Fatal("no restmail_access cookie set")
	}
	if !access.HttpOnly {
		t.Error("restmail_access cookie is not HttpOnly (JS could read the token)")
	}
	if !access.Secure {
		t.Error("restmail_access cookie is not Secure")
	}
	if access.SameSite != http.SameSiteStrictMode {
		t.Errorf("restmail_access SameSite = %v, want Strict", access.SameSite)
	}
	if access.Path != "/" {
		t.Errorf("restmail_access Path = %q, want /", access.Path)
	}
	if _, err := jwt.ValidateAccessToken(access.Value); err != nil {
		t.Errorf("restmail_access cookie is not a valid access token: %v", err)
	}

	// CSRF companion: present, readable (NOT HttpOnly), Secure, Strict.
	csrf := cookieByName(rr, auth.CSRFCookieName)
	if csrf == nil || csrf.Value == "" {
		t.Fatal("no restmail_csrf cookie set")
	}
	if csrf.HttpOnly {
		t.Error("restmail_csrf must NOT be HttpOnly (the SPA has to read it)")
	}
	if !csrf.Secure || csrf.SameSite != http.SameSiteStrictMode {
		t.Error("restmail_csrf should be Secure + SameSite=Strict")
	}

	// The token must NOT appear in the JSON body.
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, present := body.Data["access_token"]; present {
		t.Errorf("access_token must not be in the response body; body=%s", rr.Body.String())
	}
}

// TestLogout_ClearsSessionCookies: logout expires the access + CSRF cookies
// (MaxAge < 0), so the browser retains no usable session material.
func TestLogout_ClearsSessionCookies(t *testing.T) {
	jwt := auth.NewJWTService("logout-secret", 15*time.Minute, 7*24*time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)

	pair, err := jwt.GenerateTokenPair(1, "u@example.test", 1)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, "mailbox", 1)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.RefreshCookieName, Value: pair.RefreshToken})
	rr := httptest.NewRecorder()
	h.Logout(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("logout: expected 204, got %d", rr.Code)
	}
	for _, name := range []string{auth.AccessCookieName, auth.CSRFCookieName} {
		c := cookieByName(rr, name)
		if c == nil {
			t.Errorf("logout did not clear %s cookie", name)
			continue
		}
		if c.MaxAge >= 0 {
			t.Errorf("%s cookie MaxAge = %d, want < 0 (expired)", name, c.MaxAge)
		}
	}
}
