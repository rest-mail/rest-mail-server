package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
)

func refreshWith(t *testing.T, jwt *auth.JWTService, refreshToken string) *auth.Claims {
	t.Helper()
	h := NewAuthHandler(nil, jwt, "") // Refresh does not touch the DB
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "restmail_refresh", Value: refreshToken})
	rec := httptest.NewRecorder()
	h.Refresh(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	claims, err := jwt.ValidateAccessToken(resp.Data.AccessToken)
	if err != nil {
		t.Fatalf("validate refreshed access token: %v", err)
	}
	return claims
}

func TestRefresh_PreservesAdminSession(t *testing.T) {
	jwt := auth.NewJWTService("test-secret", 15*time.Minute, 7*24*time.Hour)
	pair, err := jwt.GenerateAdminTokenPair(7, "root", []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	claims := refreshWith(t, jwt, pair.RefreshToken)
	// The bug: refresh reissued a MAILBOX token, locking admins out.
	if claims.UserType != "admin" {
		t.Errorf("refreshed admin session has UserType=%q, want %q", claims.UserType, "admin")
	}
	if claims.AdminUserID != 7 {
		t.Errorf("AdminUserID=%d, want 7", claims.AdminUserID)
	}
	if len(claims.Capabilities) != 1 || claims.Capabilities[0] != "*" {
		t.Errorf("Capabilities=%v, want [*]", claims.Capabilities)
	}
}

func TestRefresh_PreservesMailboxSession(t *testing.T) {
	jwt := auth.NewJWTService("test-secret", 15*time.Minute, 7*24*time.Hour)
	pair, err := jwt.GenerateTokenPair(42, "user@example.test", 3)
	if err != nil {
		t.Fatal(err)
	}
	claims := refreshWith(t, jwt, pair.RefreshToken)
	if claims.UserType == "admin" {
		t.Error("mailbox refresh must not yield an admin token")
	}
	if claims.MailboxID != 42 {
		t.Errorf("MailboxID=%d, want 42", claims.MailboxID)
	}
}
