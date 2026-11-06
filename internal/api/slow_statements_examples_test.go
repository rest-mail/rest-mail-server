package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/rest-mail/rest-mail-server/internal/api/limittest"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// slowStatementsAbsentConnection answers as a database without
// pg_stat_statements: every statement is refused with Postgres's undefined
// table error, which the production handler turns into its 503.
type slowStatementsAbsentConnection struct{ capabilityExampleConnection }

func (c slowStatementsAbsentConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c slowStatementsAbsentConnection) Driver() driver.Driver                        { return c }
func (c slowStatementsAbsentConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c slowStatementsAbsentConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, &pgconn.PgError{Code: "42P01", Message: "private relation detail"}
}
func (c slowStatementsAbsentConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, &pgconn.PgError{Code: "42P01", Message: "private relation detail"}
}

// slowStatementsRouters are the production routers behind each refusal: failed
// reaches the handler and its database connection fails, absent reaches it and
// the extension is not there, and limited has spent its refusal budget.
type slowStatementsRouters struct {
	failed, absent, limited http.Handler
	jwt                     *auth.JWTService
}

func newSlowStatementsRouters(t *testing.T) slowStatementsRouters {
	t.Helper()
	jwt := auth.NewJWTService(routerTestSecret, 15*time.Minute, time.Hour)
	cfg := &config.Config{Environment: "test", MasterKey: accessMasterKey, APIMaxBodyBytes: 8}
	failed := NewRouters(newFailingGormDB(t), jwt, cfg, nil)
	t.Cleanup(failed.Close)
	pool := sql.OpenDB(slowStatementsAbsentConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	absent := NewRouters(db, jwt, cfg, nil)
	t.Cleanup(absent.Close)
	return slowStatementsRouters{failed: failed.Public, absent: absent.Public, limited: apiKeyRefusalLimited(t), jwt: jwt}
}

func (r slowStatementsRouters) bearer(t *testing.T, caps ...string) string {
	t.Helper()
	session, err := r.jwt.GenerateSession(testAdminHolding(caps), "operator@example.test", 0,
		[]string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	return session.AccessToken
}

// The slow statement view's refusals answer as their example pairs say, through
// the production router (#1629): no credential, a session without the
// operation's capability at the server's own scope, an order other than total
// or mean, a page size that is not an integer, an oversized request, a spent
// refusal budget, a failing database and a database without the extension.
func TestSlowStatementsExampleRefusals(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routers := newSlowStatementsRouters(t)
	paths := map[string]struct{ method, path string }{
		"listSlowStatements":  {"GET", "/api/v1/admin/database/statements"},
		"resetSlowStatements": {"POST", "/api/v1/admin/database/statements/reset"},
	}
	cases := []struct {
		op, name string
		// caps are the session's capabilities; nil sends no credential.
		caps   []string
		router http.Handler
	}{
		{"listSlowStatements", "unauthenticated", nil, routers.failed},
		{"listSlowStatements", "required_capability", []string{middleware.CapDatabaseManage, middleware.CapDomainsRead}, routers.failed},
		{"listSlowStatements", "sort_invalid", []string{middleware.CapDatabaseRead}, routers.failed},
		{"listSlowStatements", "invalid_limit", []string{middleware.CapDatabaseRead}, routers.failed},
		{"listSlowStatements", "request_too_large", []string{middleware.CapDatabaseRead}, routers.failed},
		{"listSlowStatements", "rate_limited", nil, routers.limited},
		{"listSlowStatements", "internal_error", []string{middleware.CapDatabaseRead}, routers.failed},
		{"listSlowStatements", "unavailable", []string{middleware.CapDatabaseRead}, routers.absent},
		{"resetSlowStatements", "unauthenticated", nil, routers.failed},
		{"resetSlowStatements", "required_capability", []string{middleware.CapDatabaseRead, middleware.CapDomainsRead}, routers.failed},
		{"resetSlowStatements", "request_too_large", []string{middleware.CapDatabaseManage}, routers.failed},
		{"resetSlowStatements", "rate_limited", nil, routers.limited},
		{"resetSlowStatements", "internal_error", []string{middleware.CapDatabaseManage}, routers.failed},
		{"resetSlowStatements", "unavailable", []string{middleware.CapDatabaseManage}, routers.absent},
	}
	for _, c := range cases {
		t.Run(c.op+"/"+c.name, func(t *testing.T) {
			pair, ok := pairs[c.op][c.name]
			if !ok {
				t.Fatalf("%s/%s pair missing", c.op, c.name)
			}
			examplepairs.Proven(t, c.op, c.name)
			target := paths[c.op].path
			if len(pair.Request.Query) > 0 {
				q := url.Values{}
				for k, v := range pair.Request.Query {
					raw, _ := json.Marshal(v)
					var s string
					if json.Unmarshal(raw, &s) != nil {
						s = string(raw)
					}
					q.Set(k, s)
				}
				target += "?" + q.Encode()
			}
			req := httptest.NewRequest(paths[c.op].method, target, nil)
			req.RemoteAddr = "127.0.0.1:12345"
			if c.caps != nil {
				req.Header.Set("Authorization", "Bearer "+routers.bearer(t, c.caps...))
			}
			if c.name == "request_too_large" {
				req.ContentLength = 9
			}
			rr := httptest.NewRecorder()
			c.router.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got %d, want %d: %s", rr.Code, pair.Response.Status, rr.Body)
			}
			if c.name == "rate_limited" {
				limittest.RetryAfter(t, rr.Header())
			}
			want, err := json.Marshal(pair.Response.Body)
			if err != nil {
				t.Fatal(err)
			}
			assertPublicExampleBody(t, want, rr.Body.Bytes())
		})
	}
}

// Each refusal the description publishes, sent by Vertrag to the production
// router. The two successes need pg_stat_statements, so the live run sends them.
func TestSlowStatementsExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to run the real slow statement boundary check")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers := newSlowStatementsRouters(t)
	permitted := routers.bearer(t, middleware.CapDatabaseRead, middleware.CapDatabaseManage)
	refused := routers.bearer(t, middleware.CapDomainsRead)
	const list, reset = "/api/v1/admin/database/statements", "/api/v1/admin/database/statements/reset"
	for _, tc := range []struct {
		name, status, token, method, path string
		router                            http.Handler
	}{
		{"list without a credential", "401", "", http.MethodGet, list, routers.failed},
		{"list without the capability", "403", refused, http.MethodGet, list, routers.failed},
		{"list oversized", "413", permitted, http.MethodGet, list, routers.failed},
		{"list invalid page size", "422", permitted, http.MethodGet, list, routers.failed},
		{"list budget spent", "429", "", http.MethodGet, list, routers.limited},
		{"list failing database", "500", permitted, http.MethodGet, list, routers.failed},
		{"list without the extension", "503", permitted, http.MethodGet, list, routers.absent},
		{"reset without a credential", "401", "", http.MethodPost, reset, routers.failed},
		{"reset without the capability", "403", refused, http.MethodPost, reset, routers.failed},
		{"reset oversized", "413", permitted, http.MethodPost, reset, routers.failed},
		{"reset budget spent", "429", "", http.MethodPost, reset, routers.limited},
		{"reset failing database", "500", permitted, http.MethodPost, reset, routers.failed},
		{"reset without the extension", "503", permitted, http.MethodPost, reset, routers.absent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkRegisteredExamplesWithVertrag(t, binary, tc.router, tc.token, tc.method, tc.status, []string{tc.path}, 1, tc.status == "413")
		})
	}
}
