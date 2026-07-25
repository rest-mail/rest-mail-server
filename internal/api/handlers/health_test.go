package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func openHealthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: envIntOr("DB_PORT", 5432),
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("health DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Certificate{}); err != nil {
		t.Skipf("health DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

func healthBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode health body %q: %v", rr.Body.String(), err)
	}
	return env.Data
}

// TestHealth_CertDetailGatedByAuth proves the certificate inventory is disclosed
// only to an authenticated admin. An expiring certificate is seeded so that,
// before the fix, an UNauthenticated /api/health would have surfaced it in
// cert_warnings. After the fix the unauthenticated response is liveness-only
// while a valid admin token still receives the detail. Both responses stay 200.
func TestHealth_CertDetailGatedByAuth(t *testing.T) {
	gdb := openHealthTestDB(t)

	// Seed a domain + an already-expired certificate (well within the 30-day
	// warning threshold). Unique domain name per run to dodge the unique index.
	dom := models.Domain{Name: fmt.Sprintf("health-%d.example", time.Now().UnixNano())}
	if err := gdb.Create(&dom).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	t.Cleanup(func() { gdb.Unscoped().Delete(&dom) })

	cert := models.Certificate{
		DomainID:  dom.ID,
		CertPEM:   "test",
		KeyPEM:    "test",
		Issuer:    "self-signed",
		NotBefore: time.Now().Add(-48 * time.Hour),
		NotAfter:  time.Now().Add(-24 * time.Hour),
	}
	if err := gdb.Create(&cert).Error; err != nil {
		t.Fatalf("seed certificate: %v", err)
	}
	t.Cleanup(func() { gdb.Unscoped().Delete(&cert) })

	jwtSvc := auth.NewJWTService("test-secret", 15*time.Minute, 24*time.Hour)
	h := NewHealthHandler(gdb, jwtSvc)

	// Unauthenticated: liveness only, no certificate inventory.
	unauth := httptest.NewRecorder()
	h.Health(unauth, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if unauth.Code != http.StatusOK {
		t.Fatalf("unauthenticated status = %d, want 200", unauth.Code)
	}
	body := healthBody(t, unauth)
	if body["status"] != "healthy" {
		t.Fatalf("unauthenticated response missing liveness status: %v", body)
	}
	if _, leaked := body["cert_warnings"]; leaked {
		t.Fatalf("unauthenticated /api/health disclosed cert inventory: %v", body)
	}

	// Authenticated admin: certificate inventory is included.
	pair, err := jwtSvc.GenerateAdminTokenPair(1, "admin", []string{"*"})
	if err != nil {
		t.Fatalf("mint admin token: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	authed := httptest.NewRecorder()
	h.Health(authed, req)
	if authed.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, want 200", authed.Code)
	}
	adminBody := healthBody(t, authed)
	if _, ok := adminBody["cert_warnings"]; !ok {
		t.Fatalf("authenticated admin /api/health missing cert inventory: %v", adminBody)
	}
}

// TestHealth_InvalidTokenIsLivenessOnly proves a bad/expired token does not 401
// the probe and does not unlock the detail — it degrades to liveness only.
func TestHealth_InvalidTokenIsLivenessOnly(t *testing.T) {
	gdb := openHealthTestDB(t)
	jwtSvc := auth.NewJWTService("test-secret", 15*time.Minute, 24*time.Hour)
	h := NewHealthHandler(gdb, jwtSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Authorization", "Bearer not-a-valid-token")
	rr := httptest.NewRecorder()
	h.Health(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("invalid-token status = %d, want 200 (probe must not 401)", rr.Code)
	}
	if _, leaked := healthBody(t, rr)["cert_warnings"]; leaked {
		t.Fatalf("invalid token unlocked cert inventory")
	}
}
