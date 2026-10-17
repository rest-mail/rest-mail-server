package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
)

func refreshWith(t *testing.T, jwt *auth.JWTService, refreshToken string) *auth.Claims {
	t.Helper()
	h := NewAuthHandler(nil, jwt, "", nil) // Refresh does not touch the DB
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "restmail_refresh", Value: refreshToken})
	rec := httptest.NewRecorder()
	h.Refresh(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status %d: %s", rec.Code, rec.Body.String())
	}
	// The refreshed access token is delivered as the restmail_access cookie, not
	// the response body.
	tok := accessCookieValue(rec)
	if tok == "" {
		t.Fatalf("refresh set no access cookie: %s", rec.Body.String())
	}
	claims, err := jwt.ValidateAccessToken(tok)
	if err != nil {
		t.Fatalf("validate refreshed access token: %v", err)
	}
	return claims
}

func TestRefresh_PreservesTheSession(t *testing.T) {
	jwt := auth.NewJWTService("test-secret", 15*time.Minute, 7*24*time.Hour)
	pair, err := jwt.GenerateSession(7, "root@example.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	claims := refreshWith(t, jwt, pair.RefreshToken)
	if claims.AccountID != 7 || claims.Address != "root@example.test" {
		t.Errorf("account %d %q, want 7 root@example.test", claims.AccountID, claims.Address)
	}
	// How the session signed in is kept: a refresh is not a new sign-in.
	if !claims.HasAMR(auth.AMROTP) || !claims.HasAMR(auth.AMRPassword) {
		t.Errorf("AMR=%v, want [pwd otp]", claims.AMR)
	}
}

func TestRefresh_PreservesAPasswordOnlySession(t *testing.T) {
	jwt := auth.NewJWTService("test-secret", 15*time.Minute, 7*24*time.Hour)
	pair, err := jwt.GenerateSession(42, "user@example.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	claims := refreshWith(t, jwt, pair.RefreshToken)
	if claims.AccountID != 42 || claims.TwoFactor || claims.HasAMR(auth.AMROTP) {
		t.Errorf("claims = %+v, want account 42 without a second factor", claims)
	}
}
