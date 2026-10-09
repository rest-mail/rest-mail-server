package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// nativeBody decodes the token fields of a login/refresh success body.
type nativeBody struct {
	Data struct {
		ExpiresIn        int    `json:"expires_in"`
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		RefreshExpiresIn int    `json:"refresh_expires_in"`
	} `json:"data"`
}

func decodeNative(t *testing.T, rr *httptest.ResponseRecorder) nativeBody {
	t.Helper()
	var b nativeBody
	if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rr.Body.String())
	}
	return b
}

// bodyRequest builds a POST carrying {"refresh_token": token} and no cookies, as
// the mobile client sends it.
func bodyRequest(path, token string) *http.Request {
	b, _ := json.Marshal(refreshRequest{RefreshToken: token})
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestRefresh_NativeBodyTokenGetsBodyTokens: #9. A refresh token sent in the
// body is accepted, and the replacement pair comes back in the body with no
// cookies set, so a native client never handles cookie names or paths.
func TestRefresh_NativeBodyTokenGetsBodyTokens(t *testing.T) {
	jwt := auth.NewJWTService("native-secret", 15*time.Minute, 7*24*time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)

	pair, err := jwt.GenerateTokenPair(42, "user@example.test", 3)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, "mailbox", 42)

	rr := httptest.NewRecorder()
	h.Refresh(rr, bodyRequest("/api/v1/auth/refresh", pair.RefreshToken))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if cookies := rr.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("native refresh set %d cookies, want none", len(cookies))
	}
	b := decodeNative(t, rr)
	if _, err := jwt.ValidateAccessToken(b.Data.AccessToken); err != nil {
		t.Errorf("body access token invalid: %v", err)
	}
	if b.Data.RefreshToken == "" || b.Data.RefreshToken == pair.RefreshToken {
		t.Errorf("expected a new refresh token in the body, got %q", b.Data.RefreshToken)
	}
	if b.Data.RefreshExpiresIn <= 0 {
		t.Errorf("refresh_expires_in = %d, want > 0", b.Data.RefreshExpiresIn)
	}

	// Rotation holds on the body channel too: the old token is spent, the new
	// one works.
	rr2 := httptest.NewRecorder()
	h.Refresh(rr2, bodyRequest("/api/v1/auth/refresh", pair.RefreshToken))
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("reuse of rotated token: expected 401, got %d", rr2.Code)
	}
	rr3 := httptest.NewRecorder()
	h.Refresh(rr3, bodyRequest("/api/v1/auth/refresh", b.Data.RefreshToken))
	if rr3.Code != http.StatusOK {
		t.Fatalf("refresh with rotated token: expected 200, got %d (%s)", rr3.Code, rr3.Body.String())
	}
}

// TestRefresh_CookieWinsOverBody: #9. A request carrying the refresh cookie is a
// browser's. It is answered with cookies and a token-free body even when it also
// sends a body token, so page script cannot trade the httpOnly cookie for a
// readable token.
func TestRefresh_CookieWinsOverBody(t *testing.T) {
	jwt := auth.NewJWTService("native-secret", 15*time.Minute, 7*24*time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)

	cookiePair, err := jwt.GenerateTokenPair(1, "a@example.test", 1)
	if err != nil {
		t.Fatal(err)
	}
	bodyPair, err := jwt.GenerateTokenPair(2, "b@example.test", 2)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, cookiePair, "mailbox", 1)
	seedActive(store, bodyPair, "mailbox", 2)

	req := bodyRequest("/api/v1/auth/refresh", bodyPair.RefreshToken)
	req.AddCookie(&http.Cookie{Name: auth.RefreshCookieName, Value: cookiePair.RefreshToken})
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if refreshCookieValue(rr) == "" || accessCookieValue(rr) == "" {
		t.Error("cookie refresh did not answer with cookies")
	}
	if b := decodeNative(t, rr); b.Data.AccessToken != "" || b.Data.RefreshToken != "" {
		t.Error("cookie refresh leaked tokens into the body")
	}
	if got := store.statusOf(cookiePair.RefreshJTI); got != models.RefreshTokenRotated {
		t.Errorf("cookie token status = %q, want rotated", got)
	}
	if got := store.statusOf(bodyPair.RefreshJTI); got != models.RefreshTokenActive {
		t.Errorf("body token status = %q, want untouched (active)", got)
	}
}

// TestLogout_NativeBodyTokenRevoked: #9. Logout revokes a refresh token sent in
// the body, so a native client can end its session without building a Cookie
// header.
func TestLogout_NativeBodyTokenRevoked(t *testing.T) {
	jwt := auth.NewJWTService("native-secret", 15*time.Minute, 7*24*time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)

	pair, err := jwt.GenerateTokenPair(5, "c@example.test", 5)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, "mailbox", 5)

	rr := httptest.NewRecorder()
	h.Logout(rr, bodyRequest("/api/v1/auth/logout", pair.RefreshToken))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if got := store.statusOf(pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Errorf("status after logout = %q, want revoked", got)
	}
}

// TestRefresh_NoTokenAnywhere: an empty or tokenless body is still "No refresh
// token", as before.
func TestRefresh_NoTokenAnywhere(t *testing.T) {
	jwt := auth.NewJWTService("native-secret", 15*time.Minute, 7*24*time.Hour)
	h := newRefreshHandler(jwt, newFakeRefreshStore())
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil),
		bodyRequest("/api/v1/auth/refresh", ""),
	} {
		rr := httptest.NewRecorder()
		h.Refresh(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rr.Code)
		}
	}
}

// TestLogin_UnknownClientRefused: a client value other than "native" is a
// validation error, not a silent fallback to cookies. Rejected before any
// database lookup, so it runs without one.
func TestLogin_UnknownClientRefused(t *testing.T) {
	h := &AuthHandler{}
	rr := doLogin(h, map[string]string{"email": "x@example.test", "password": "pw", "client": "nativ"})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestLogin_NativeGetsBodyTokens: #9. A login with "client": "native" returns the
// token pair in the body and sets no cookies; the refresh token is recorded in
// the rotation ledger like any other.
func TestLogin_NativeGetsBodyTokens(t *testing.T) {
	gdb := openLoginTestDB(t)
	jwtSvc := auth.NewJWTService("login-secret", 15*time.Minute, 7*24*time.Hour)
	h := NewAuthHandler(gdb, jwtSvc, "")

	addr := seedLoginMailbox(t, gdb, "correct-horse-battery")
	rr := doLogin(h, map[string]string{"email": addr, "password": "correct-horse-battery", "client": "native"})
	if rr.Code != http.StatusOK {
		t.Fatalf("login: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if cookies := rr.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("native login set %d cookies, want none", len(cookies))
	}
	b := decodeNative(t, rr)
	if _, err := jwtSvc.ValidateAccessToken(b.Data.AccessToken); err != nil {
		t.Errorf("body access token invalid: %v", err)
	}
	claims, err := jwtSvc.ValidateRefreshToken(b.Data.RefreshToken)
	if err != nil {
		t.Fatalf("body refresh token invalid: %v", err)
	}
	t.Cleanup(func() { gdb.Where("jti = ?", claims.ID).Delete(&models.RefreshToken{}) })
	if rec, err := h.refreshStore.(interface {
		GetByJTI(string) (*models.RefreshToken, error)
	}).GetByJTI(claims.ID); err != nil || rec.Status != models.RefreshTokenActive {
		t.Errorf("native refresh token not active in ledger: %v", err)
	}
}
