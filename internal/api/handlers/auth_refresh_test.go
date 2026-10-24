package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
)

// validateBody reads the refreshed access token from the restmail_access cookie
// (the token is no longer returned in the response body) and returns its
// validated claims.
func validateBody(t *testing.T, jwt *auth.JWTService, rr *httptest.ResponseRecorder) *auth.Claims {
	t.Helper()
	tok := accessCookieValue(rr)
	if tok == "" {
		t.Fatalf("refresh set no access cookie (body: %s)", rr.Body.String())
	}
	claims, err := jwt.ValidateAccessToken(tok)
	if err != nil {
		t.Fatalf("validate refreshed access token: %v", err)
	}
	return claims
}

// accessCookieValue returns the value of the restmail_access cookie set on a
// response, or "" when absent.
func accessCookieValue(rr *httptest.ResponseRecorder) string {
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.AccessCookieName {
			return c.Value
		}
	}
	return ""
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

func (f *fakeRefreshStore) RevokeAllForAccount(accountID uint) error {
	for _, rec := range f.rows {
		if rec.AccountID == accountID && rec.Status == models.RefreshTokenActive {
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
	serveAuth(h, rr, req)
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

func seedActive(store *fakeRefreshStore, pair *auth.TokenPair, accountID uint) {
	_ = store.Save(&models.RefreshToken{
		Jti:       pair.RefreshJTI,
		AccountID: accountID,
		SessionID: pair.SessionID,
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

	pair, err := jwt.GenerateSession(42, "user@example.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, 42)

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

	pair, err := jwt.GenerateSession(1, "u@example.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, 1)

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

	pair, err := jwt.GenerateSession(7, "root@example.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedActive(store, pair, 7)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "restmail_refresh", Value: pair.RefreshToken})
	rr := httptest.NewRecorder()
	serveAuth(h, rr, req)
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

	pair, err := jwt.GenerateSession(5, "ghost@example.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Intentionally do NOT seed the ledger.
	if rr := doRefresh(h, pair.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh of unknown jti: expected 401, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestRefresh_PreservesTheSessionWithRotation: a refresh through rotation
// keeps the account, and the session's sign-in methods.
func TestRefresh_PreservesTheSessionWithRotation(t *testing.T) {
	jwt := auth.NewJWTService("preserve-secret", 15*time.Minute, 7*24*time.Hour)

	t.Run("password", func(t *testing.T) {
		store := newFakeRefreshStore()
		h := newRefreshHandler(jwt, store)
		pair, err := jwt.GenerateSession(9, "ops@example.test", 0, []string{auth.AMRPassword}, false)
		if err != nil {
			t.Fatal(err)
		}
		seedActive(store, pair, 9)
		rr := doRefresh(h, pair.RefreshToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("refresh: %d (%s)", rr.Code, rr.Body.String())
		}
		claims := validateBody(t, jwt, rr)
		if claims.AccountID != 9 || claims.Address != "ops@example.test" {
			t.Errorf("claims not preserved: %+v", claims)
		}
		if len(claims.AMR) != 1 || claims.AMR[0] != auth.AMRPassword {
			t.Errorf("amr not preserved: %v", claims.AMR)
		}
	})

	t.Run("another account", func(t *testing.T) {
		store := newFakeRefreshStore()
		h := newRefreshHandler(jwt, store)
		pair, err := jwt.GenerateSession(21, "user@example.test", 0, []string{auth.AMRPassword}, false)
		if err != nil {
			t.Fatal(err)
		}
		seedActive(store, pair, 21)
		rr := doRefresh(h, pair.RefreshToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("mailbox refresh: %d (%s)", rr.Code, rr.Body.String())
		}
		claims := validateBody(t, jwt, rr)
		if claims.AccountID != 21 {
			t.Errorf("claims not preserved: %+v", claims)
		}
	})
}
