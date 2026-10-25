package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/grants"
)

var authReadPaths = map[string]string{
	"getTwoFactorStatus": "/api/v1/auth/2fa",
	"listCapabilities":   "/api/v1/admin/capabilities",
}

// Only the session/permission backends and capability rows are controlled.
// JWT, CSRF, body-limit and capability middleware, registrations, handlers,
// GORM's row mapping and response writers are the production implementations.
func authReadExampleRouter(t *testing.T, jwt *auth.JWTService, operation string, db *gorm.DB, allowed bool) http.Handler {
	t.Helper()
	router := chi.NewRouter()
	router.Use(middleware.JWTMiddleware(jwt))
	router.Use(middleware.RejectStaleSessions(func(uint, int) (bool, error) { return true, nil }))
	if operation == "listCapabilities" {
		router.Use(middleware.SetupDone)
	}
	router.Use(middleware.CSRF())
	router.Use(middleware.MaxBodyBytes(8))
	rs := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	pass := endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	if operation == "getTwoFactorStatus" {
		handlers.NewTwoFactorHandler(db, "", true, nil).RegisterOwn(rs, handlers.SessionRouteChecks{
			Throttle: pass, Audit: func(string, ...middleware.AuditOption) endpoint.Middleware { return pass },
		})
	} else {
		held := func(uint) (grants.Held, error) {
			if allowed {
				return grants.HeldOf(grants.Entry{Capability: middleware.CapUsersRead, Scope: grants.All}), nil
			}
			return grants.Held{}, nil
		}
		handlers.NewGrantHandler(db, nil, "").Register(rs, handlers.GrantRouteChecks{
			At: func(capability string) endpoint.Middleware {
				return endpoint.Middleware{Wrap: middleware.RequireCapability(held, capability, nil)}
			}, SessionOnly: pass, AuthThrottle: pass,
		})
	}
	return router
}

func TestAuthenticatedReadExamplesMatchRegisteredRefusals(t *testing.T) {
	// The package's ordinary authorization tests stand these DB lookups aside.
	// Restore the production builders so the503 cases reach the real session cache.
	useRealGrants(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	production, jwt := newTestRouter(t)
	token := mailboxToken(t, jwt)
	for operation, path := range authReadPaths {
		for _, name := range []string{"unauthenticated", "session_unavailable", "request_too_large", "internal_error", "insufficient_permissions"} {
			if operation == "getTwoFactorStatus" && name == "insufficient_permissions" {
				continue // safe GET has neither a capability nor a CSRF refusal
			}
			t.Run(operation+"/"+name, func(t *testing.T) {
				pair, ok := pairs[operation][name]
				if !ok {
					t.Fatal("the named refusal pair is missing")
				}
				req := httptest.NewRequest(http.MethodGet, path, nil)
				var router http.Handler = production
				if name != "unauthenticated" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				if name != "unauthenticated" && name != "session_unavailable" {
					router = authReadExampleRouter(t, jwt, operation, newFailingGormDB(t), name != "insufficient_permissions")
				}
				if name == "request_too_large" {
					req.ContentLength = 9 // transport cap applies even though GET binds no body
				}
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %v: %s", rr.Code, rr.Header(), rr.Body.String())
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, warning := range describedRouters(t).generator("authenticated reads").Report() {
		if _, selected := authReadPaths[warning.OperationID]; selected {
			t.Errorf("authenticated read still has a generator warning: %v", warning)
		}
	}
}

func TestCapabilitiesExampleMatchesRegisteredRows(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	router := authReadExampleRouter(t, jwt, "listCapabilities", capabilityExampleDB(t), true)
	req := httptest.NewRequest(http.MethodGet, authReadPaths["listCapabilities"], nil)
	req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	pair := pairs["listCapabilities"]["available"]
	if rr.Code != pair.Response.Status {
		t.Fatalf("capability read = %d: %s", rr.Code, rr.Body.String())
	}
	assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
}

func TestCSRFSafeReadsAndUnsafeDescriptionsAgree(t *testing.T) {
	paths, _ := describedAPI(t)
	if _, present := paths["/api/v1/auth/2fa"]["get"].Responses["403"]; present {
		t.Fatal("a safe own-status read still declares an unreachable403")
	}
	if _, present := paths["/api/v1/auth/2fa/enroll"]["post"].Responses["403"]; !present {
		t.Fatal("the protected enrollment lost its403")
	}
	if _, present := paths["/api/v1/admin/capabilities"]["get"].Responses["403"]; !present {
		t.Fatal("the capability-gated read lost its real403")
	}
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	router := authReadExampleRouter(t, jwt, "getTwoFactorStatus", newFailingGormDB(t), true)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path, status := "/api/v1/auth/2fa", http.StatusInternalServerError
		if method == http.MethodPost {
			path, status = path+"/enroll", http.StatusForbidden
		}
		req := httptest.NewRequest(method, path, nil)
		req.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: mailboxToken(t, jwt)})
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != status {
			t.Fatalf("cookie %s without CSRF = %d, want%d: %s", method, rr.Code, status, rr.Body.String())
		}
	}
}

// This SQL connector supplies one capability row to the actual GORM reader.
// It tests transport mapping, not a live PostgreSQL installation or grants DB.
type capabilityExampleConnection struct{}

func (c capabilityExampleConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c capabilityExampleConnection) Driver() driver.Driver                        { return c }
func (c capabilityExampleConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c capabilityExampleConnection) Close() error                                 { return nil }
func (c capabilityExampleConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement in capability read")
}
func (c capabilityExampleConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction in capability read")
}
func (c capabilityExampleConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, `"capabilities"`) {
		return nil, errors.New("unexpected table in capability example")
	}
	return &capabilityExampleRows{}, nil
}

type capabilityExampleRows struct{ sent bool }

func (*capabilityExampleRows) Columns() []string {
	return []string{"id", "name", "description", "resource", "action"}
}
func (*capabilityExampleRows) Close() error { return nil }
func (r *capabilityExampleRows) Next(values []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	copy(values, []driver.Value{int64(1), "users:read", "Read accounts and grants", "users", "read"})
	r.sent = true
	return nil
}

func capabilityExampleDB(t *testing.T) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(capabilityExampleConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{
		DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	return db
}
