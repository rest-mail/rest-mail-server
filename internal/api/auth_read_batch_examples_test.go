package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/pem"
	"errors"
	"github.com/restmail/restmail/internal/examplepairs"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
)

var authReadBatchPaths = map[string]string{
	"getDashboardStats": "/api/v1/admin/stats",
	"getQueueStats":     "/api/v1/admin/queue/stats",
	"listDKIMKeys":      "/api/v1/admin/dkim",
	"listMyGrants":      "/api/v1/admin/grants/mine",
	"listPushDevices":   "/api/v1/devices",
	"listRoles":         "/api/v1/admin/roles",
}

// Enable the optional device registration without contacting APNs. No push is
// sent by these reads; this key is only a local constructor prerequisite.
func configureAuthReadPush(t *testing.T) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "read-apns.p8")
	if err := os.WriteFile(name, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESTMAIL_APNS_KEY_FILE", name)
	t.Setenv("RESTMAIL_APNS_KEY_ID", "REPORTKEY1")
	t.Setenv("RESTMAIL_APNS_TEAM_ID", "REPORTTEAM")
	t.Setenv("RESTMAIL_APNS_TOPIC", "")
}

func authReadBatchRouters(t *testing.T) (http.Handler, http.Handler, http.Handler, *auth.JWTService) {
	t.Helper()
	configureAuthReadPush(t)
	useRealGrants(t)
	// Scenario tokens are minted once per test, and a Vertrag pair test can run
	// past a minute: give them the documented 15-minute access lifetime.
	jwt := auth.NewJWTService(routerTestSecret, 15*time.Minute, time.Hour)
	newRouter := func(db *gorm.DB) http.Handler {
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
		t.Cleanup(r.Close)
		return r.Public
	}
	// Construct this one before replacing the test seams: its cache really
	// queries the unavailable database and produces the described503.
	unavailable := newRouter(newFailingGormDB(t))
	sessionCheck = func(*handlers.SessionCache) middleware.SessionCurrent {
		return func(uint, int) (bool, error) { return true, nil }
	}
	heldGrants = func(*grants.Cache) middleware.HeldGrants {
		return func(uint) (grants.Held, error) {
			return grants.HeldOf(grants.Entry{Capability: grants.Wildcard, Scope: grants.All}), nil
		}
	}
	return unavailable, newRouter(newFailingGormDB(t)), newRouter(authReadSnapshotDB(t)), jwt
}

// These rows are a fixed read snapshot, supplied through database/sql and
// mapped by the real GORM queries. No PostgreSQL/query-semantics claim is made.
// Counts and lists are empty except two older deliveries in the queue. A fixed
// volume row avoids the handler's time-dependent seven-empty-days fallback.
type authReadSnapshotConnection struct{ capabilityExampleConnection }

func (c authReadSnapshotConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c authReadSnapshotConnection) Driver() driver.Driver                        { return c }
func (c authReadSnapshotConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c authReadSnapshotConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	switch {
	case strings.Contains(q, "created_at::date"):
		return &authReadSnapshotRows{columns: []string{"date", "count"}, rows: [][]driver.Value{{time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), int64(2)}}}, nil
	case strings.Contains(q, "as total_inbound_mx"):
		return &authReadSnapshotRows{columns: []string{"total_inbound_mx", "over_tls", "plaintext", "plaintext_auth_pass", "plaintext_auth_fail"}, rows: [][]driver.Value{{int64(0), int64(0), int64(0), int64(0), int64(0)}}}, nil
	case strings.HasPrefix(q, "select count(*)"):
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}, nil
	case strings.HasPrefix(q, "select status,") && strings.Contains(q, "group by"):
		return &authReadSnapshotRows{columns: []string{"status", "count"}, rows: [][]driver.Value{{"delivered", int64(2)}}}, nil
	}
	for _, table := range []string{"domains", "mailboxes", "outbound_queue", "grant_activations", "grants", "roles", "push_devices"} {
		if strings.HasPrefix(q, "select ") && strings.Contains(q, `"`+table+`"`) {
			return &authReadSnapshotRows{columns: []string{"id"}}, nil
		}
	}
	return nil, errors.New("unexpected SQL in authenticated read snapshot")
}

type authReadSnapshotRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *authReadSnapshotRows) Columns() []string { return r.columns }
func (*authReadSnapshotRows) Close() error        { return nil }
func (r *authReadSnapshotRows) Next(values []driver.Value) error {
	if r.index == len(r.rows) {
		return io.EOF
	}
	copy(values, r.rows[r.index])
	r.index++
	return nil
}

func authReadSnapshotDB(t *testing.T) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(authReadSnapshotConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAuthenticatedReadBatchExamples(t *testing.T) {
	unavailable, failed, snapshot, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	pending, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range authReadBatchPaths {
		for _, tc := range []struct {
			name   string
			status int
			token  string
			router http.Handler
		}{
			{"available", 200, token, snapshot},
			{"unauthenticated", 401, "", unavailable},
			{"setup_required", 403, pending.AccessToken, failed},
			{"request_too_large", 413, token, failed},
			{"internal_error", 500, token, failed},
			{"session_unavailable", 503, token, unavailable},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				if tc.token != "" {
					req.Header.Set("Authorization", "Bearer "+tc.token)
				}
				if tc.status == 413 {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				tc.router.ServeHTTP(rr, req)
				if rr.Code != tc.status {
					t.Fatalf("got %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
				}
				pair, ok := pairs[operation][tc.name]
				if !ok {
					t.Fatal("named response pair is missing")
				}
				if pair.Response.Status != rr.Code || pair.Response.MediaType != rr.Header().Get("Content-Type") {
					t.Fatal("response metadata differs from its pair")
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, selected := authReadBatchPaths[warning.OperationID]; selected {
				t.Errorf("batch operation still has a generator warning: %v", warning)
			}
		}
	}
}
