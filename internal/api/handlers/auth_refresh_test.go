package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
)

// validateBody decodes the access_token from a refresh response body and returns
// its validated claims.
func validateBody(t *testing.T, jwt *auth.JWTService, rr *httptest.ResponseRecorder) *auth.Claims {
	t.Helper()
	var resp struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode refresh response: %v", err)
	}
	claims, err := jwt.ValidateAccessToken(resp.Data.AccessToken)
	if err != nil {
		t.Fatalf("validate refreshed access token: %v", err)
	}
	return claims
}

// fakeRefreshStore is an in-memory refreshTokenStore for exercising the auth
// handler's rotation/revocation state machine without a database. It mirrors the
// gorm repository's contract: Rotate is single-use (active→rotated), Revoke is
// idempotent, and a missing/non-active jti is ErrRefreshTokenNotFound.
type fakeRefreshStore struct {
	rows map[string]*models.RefreshToken
}

func newFakeRefreshStore() *fakeRefreshStore {
	return &fakeRefreshStore{rows: map[string]*models.RefreshToken{}}
}

func (f *fakeRefreshStore) Save(rec *models.RefreshToken) error {
	cp := *rec
	f.rows[rec.Jti] = &cp
	return nil
}

func (f *fakeRefreshStore) Rotate(jti string) error {
	rec, ok := f.rows[jti]
	if !ok || rec.Status != models.RefreshTokenActive {
		return repositories.ErrRefreshTokenNotFound
	}
	rec.Status = models.RefreshTokenRotated
	return nil
}

func (f *fakeRefreshStore) Revoke(jti string) error {
	if rec, ok := f.rows[jti]; ok {
		rec.Status = models.RefreshTokenRevoked
	}
	return nil
}

func (f *fakeRefreshStore) RevokeAllForSubject(userType string, subjectID uint) error {
	for _, rec := range f.rows {
		if rec.UserType == userType && rec.SubjectID == subjectID && rec.Status == models.RefreshTokenActive {
			rec.Status = models.RefreshTokenRevoked
		}
	}
	return nil
}

func (f *fakeRefreshStore) statusOf(jti string) string {
	if rec, ok := f.rows[jti]; ok {
		return rec.Status
	}
	return ""
}

// newRefreshHandler builds an AuthHandler wired to a fake store and no DB. The
// refresh/logout paths never touch the DB, so nil is safe here.
func newRefreshHandler(jwt *auth.JWTService, store refreshTokenStore) *AuthHandler {
	return &AuthHandler{db: nil, jwtService: jwt, refreshStore: store}
}

func doRefresh(h *AuthHandler, refreshToken string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "restmail_refresh", Value: refreshToken})
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)
	return rr
}

func refreshCookieValue(rr *httptest.ResponseRecorder) string {
	for _, c := range rr.Result().Cookies() {
		if c.Name == "restmail_refresh" {
			return c.Value
		}
	}
	return ""
}

func seedActive(store *fakeRefreshStore, pair *auth.TokenPair, userType string, subjectID uint) {
	_ = store.Save(&models.RefreshToken{
		Jti:       pair.RefreshJTI,
		UserType:  userType,
		SubjectID: subjectID,
		Status:    models.RefreshTokenActive,
		ExpiresAt: pair.RefreshExpiresAt,
	})
}

// TestRefresh_RotationInvalidatesOldToken: OSI-10. A successful refresh rotates
// the presented refresh token — a new one is issued and the old one can no
// longer be exchanged, while the replacement works exactly once itself.
func TestRefresh_RotationInvalidatesOldToken(t *testing.T) {
	jwt := auth.NewJWTService("rotation-secret", 15*time.Minute, 7*24*time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)

	pair, err := jwt.GenerateTokenPair(42, "user@example.test", 3)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, "mailbox", 42)

	// First refresh succeeds and hands back a rotated refresh cookie.
	rr := doRefresh(h, pair.RefreshToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("first refresh: expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	rotated := refreshCookieValue(rr)
	if rotated == "" || rotated == pair.RefreshToken {
		t.Fatalf("expected a new (rotated) refresh token, got %q", rotated)
	}
	if got := store.statusOf(pair.RefreshJTI); got != models.RefreshTokenRotated {
		t.Errorf("old token status = %q, want rotated", got)
	}

	// Reusing the OLD refresh token is now refused (rotated, not active).
	if rr2 := doRefresh(h, pair.RefreshToken); rr2.Code != http.StatusUnauthorized {
		t.Fatalf("reuse of rotated token: expected 401, got %d (%s)", rr2.Code, rr2.Body.String())
	}

	// The NEW refresh token is active and works.
	if rr3 := doRefresh(h, rotated); rr3.Code != http.StatusOK {
		t.Fatalf("refresh with rotated token: expected 200, got %d (%s)", rr3.Code, rr3.Body.String())
	}
}

// TestRefresh_RevokedTokenBlocked: OSI-10. A revoked refresh token cannot be
// exchanged, even though its JWT signature and expiry are still valid.
func TestRefresh_RevokedTokenBlocked(t *testing.T) {
	jwt := auth.NewJWTService("revoke-secret", 15*time.Minute, 7*24*time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)

	pair, err := jwt.GenerateTokenPair(1, "u@example.test", 1)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, "mailbox", 1)

	if err := store.Revoke(pair.RefreshJTI); err != nil {
		t.Fatal(err)
	}

	if rr := doRefresh(h, pair.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh of revoked token: expected 401, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestLogout_RevokesRefreshToken: OSI-10. Logout revokes the presented refresh
// token server-side, so it can no longer be refreshed (logout was previously
// client-side only).
func TestLogout_RevokesRefreshToken(t *testing.T) {
	jwt := auth.NewJWTService("logout-secret", 15*time.Minute, 7*24*time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)

	pair, err := jwt.GenerateAdminTokenPair(7, "root", []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, "admin", 7)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "restmail_refresh", Value: pair.RefreshToken})
	rr := httptest.NewRecorder()
	h.Logout(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("logout: expected 204, got %d", rr.Code)
	}
	if got := store.statusOf(pair.RefreshJTI); got != models.RefreshTokenRevoked {
		t.Errorf("post-logout status = %q, want revoked", got)
	}

	// The logged-out token can no longer be refreshed.
	if rr2 := doRefresh(h, pair.RefreshToken); rr2.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: expected 401, got %d (%s)", rr2.Code, rr2.Body.String())
	}
}

// TestRefresh_UnknownJTIRejected: a signed, unexpired refresh token whose jti is
// not in the ledger (never issued / already pruned) is refused — the ledger is
// the source of truth (fail closed).
func TestRefresh_UnknownJTIRejected(t *testing.T) {
	jwt := auth.NewJWTService("unknown-secret", 15*time.Minute, 7*24*time.Hour)
	store := newFakeRefreshStore()
	h := newRefreshHandler(jwt, store)

	pair, err := jwt.GenerateTokenPair(5, "ghost@example.test", 2)
	if err != nil {
		t.Fatal(err)
	}
	// Intentionally do NOT seed the ledger.
	if rr := doRefresh(h, pair.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh of unknown jti: expected 401, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestRefresh_PreservesTypeWithRotation: the #45 session-type preservation holds
// through rotation — an admin refresh yields an admin token (with capabilities),
// a mailbox refresh a mailbox token.
func TestRefresh_PreservesTypeWithRotation(t *testing.T) {
	jwt := auth.NewJWTService("preserve-secret", 15*time.Minute, 7*24*time.Hour)

	t.Run("admin", func(t *testing.T) {
		store := newFakeRefreshStore()
		h := newRefreshHandler(jwt, store)
		pair, err := jwt.GenerateAdminTokenPair(9, "ops", []string{"queue:read"})
		if err != nil {
			t.Fatal(err)
		}
		seedActive(store, pair, "admin", 9)
		rr := doRefresh(h, pair.RefreshToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("admin refresh: %d (%s)", rr.Code, rr.Body.String())
		}
		claims := validateBody(t, jwt, rr)
		if claims.UserType != "admin" || claims.AdminUserID != 9 {
			t.Errorf("admin claims not preserved: %+v", claims)
		}
		if len(claims.Capabilities) != 1 || claims.Capabilities[0] != "queue:read" {
			t.Errorf("capabilities not preserved: %v", claims.Capabilities)
		}
	})

	t.Run("mailbox", func(t *testing.T) {
		store := newFakeRefreshStore()
		h := newRefreshHandler(jwt, store)
		pair, err := jwt.GenerateTokenPair(21, "user@example.test", 4)
		if err != nil {
			t.Fatal(err)
		}
		seedActive(store, pair, "mailbox", 21)
		rr := doRefresh(h, pair.RefreshToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("mailbox refresh: %d (%s)", rr.Code, rr.Body.String())
		}
		claims := validateBody(t, jwt, rr)
		if claims.UserType == "admin" || claims.MailboxID != 21 {
			t.Errorf("mailbox claims not preserved: %+v", claims)
		}
	})
}
