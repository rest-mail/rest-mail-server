package api

import (
	"crypto/tls"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/mtls"
	"github.com/restmail/restmail/internal/mtls/mtlstest"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func failingDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(failConnector{}),
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open gorm: %v", err)
	}
	return gdb
}

func mtlsRouters(t *testing.T, p *mtlstest.Paths) *Routers {
	t.Helper()
	jwtSvc := auth.NewJWTService("router-test-secret", 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins:     []string{"http://localhost:3000"},
		Environment:            "test",
		InternalMTLSEnabled:    true,
		InternalMTLSCACert:     p.CACert,
		InternalMTLSServerCert: p.ServerCert,
		InternalMTLSServerKey:  p.ServerKey,
	}
	return NewRouters(failingDB(t), jwtSvc, cfg, nil)
}

// TestInternalMTLS_RoutesWithheldFromPublic proves the security-relevant
// routing split: when internal mTLS is enabled the gateway-facing routes are
// NOT on the public handler (404), and the internal handler requires a client
// cert (401 for a plain, certless request).
func TestInternalMTLS_RoutesWithheldFromPublic(t *testing.T) {
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write material: %v", err)
	}
	routers := mtlsRouters(t, p)
	if routers.Internal == nil {
		t.Fatal("routers.Internal is nil with mTLS enabled")
	}

	// Public handler must no longer expose the internal routes. A withheld
	// route yields a routing-level rejection: 404 when nothing else matches the
	// path (GET /api/mailboxes), or 405 when a sibling pattern claims the path
	// for other methods (POST /api/v1/messages/deliver is shadowed by
	// /api/v1/messages/{id}). Either proves the handler is not wired here.
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/mailboxes?address=a@b.test"},
		{http.MethodPost, "/api/v1/messages/deliver"},
	} {
		rec := httptest.NewRecorder()
		routers.Public.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("public %s %s = %d, want 404/405 (route should be internal-only)", tc.method, tc.path, rec.Code)
		}
	}

	// Internal handler exists but rejects a certless (non-TLS) request.
	rec := httptest.NewRecorder()
	routers.Internal.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mailboxes?address=a@b.test", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("internal route without client cert = %d, want 401", rec.Code)
	}
}

// TestInternalMTLS_PublicKeepsRoutesWhenDisabled proves the non-breaking
// fallback: with mTLS disabled the routes stay on the public handler (they
// reach the handler rather than 404), and no internal handler is built.
func TestInternalMTLS_PublicKeepsRoutesWhenDisabled(t *testing.T) {
	jwtSvc := auth.NewJWTService("router-test-secret", 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins: []string{"http://localhost:3000"},
		Environment:        "test",
	}
	routers := NewRouters(failingDB(t), jwtSvc, cfg, nil)
	if routers.Internal != nil {
		t.Error("routers.Internal should be nil when mTLS disabled")
	}
	rec := httptest.NewRecorder()
	routers.Public.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mailboxes?address=a@b.test", nil))
	if rec.Code == http.StatusNotFound {
		t.Error("public /api/mailboxes = 404 with mTLS disabled, want the route to be served")
	}
}

// TestInternalMTLS_ListenerEndToEnd serves routers.Internal over a real mTLS
// listener and drives it with a raw HTTP client: a valid gateway cert reaches
// the handler (past the mTLS gate + RequireClientCert); no cert fails at the
// handshake.
func TestInternalMTLS_ListenerEndToEnd(t *testing.T) {
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write material: %v", err)
	}
	routers := mtlsRouters(t, p)

	serverCfg, err := mtls.ServerTLSConfig(p.CACert, p.ServerCert, p.ServerKey)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	ts := httptest.NewUnstartedServer(routers.Internal)
	ts.TLS = serverCfg
	ts.StartTLS()
	defer ts.Close()

	t.Run("valid gateway cert reaches the handler", func(t *testing.T) {
		clientCfg, err := mtls.ClientTLSConfig(p.CACert, p.ClientCert, p.ClientKey)
		if err != nil {
			t.Fatalf("ClientTLSConfig: %v", err)
		}
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: clientCfg}, Timeout: 5 * time.Second}
		resp, err := c.Get(ts.URL + "/api/mailboxes?address=a@b.test")
		if err != nil {
			t.Fatalf("request with valid cert failed: %v", err)
		}
		defer resp.Body.Close()
		// The failing DB makes the handler answer 5xx/4xx, but crucially NOT the
		// mTLS-gate rejections: reaching the handler is the proof.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
			t.Fatalf("valid cert got %d, expected to reach the handler", resp.StatusCode)
		}
	})

	t.Run("no client cert fails the handshake", func(t *testing.T) {
		// Trust the server CA (so the failure is attributable to the missing
		// client cert, not to server verification), but present no client cert.
		trusting, err := mtls.ClientTLSConfig(p.CACert, p.ClientCert, p.ClientKey)
		if err != nil {
			t.Fatalf("ClientTLSConfig: %v", err)
		}
		clientCfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: trusting.RootCAs}
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: clientCfg}, Timeout: 5 * time.Second}
		if resp, err := c.Get(ts.URL + "/api/mailboxes?address=a@b.test"); err == nil {
			resp.Body.Close()
			t.Fatalf("expected handshake failure without a client cert, got %d", resp.StatusCode)
		}
	})
}
