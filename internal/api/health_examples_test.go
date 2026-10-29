package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/handlers"
)

// The public probe needs only database/sql's Ping. An in-process connector
// controls that dependency without requiring PostgreSQL or inventing a health
// handler. Unexpected SQL queries fail instead of returning fabricated rows.
type healthProbeConnection struct{ pingError error }

func (c healthProbeConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c healthProbeConnection) Driver() driver.Driver                        { return c }
func (c healthProbeConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c healthProbeConnection) Close() error                                 { return nil }
func (c healthProbeConnection) Ping(context.Context) error                   { return c.pingError }
func (c healthProbeConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("public health examples must not query database rows")
}
func (c healthProbeConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("public health examples must not begin transactions")
}

func healthExampleRouter(t *testing.T, pingError error) http.Handler {
	t.Helper()
	return registeredHealthExampleRouter(healthProbeDB(t, pingError))
}

// healthProbeDB is a GORM handle over the probe connection: Ping answers pingError and
// every query fails, as it would against a database that is down.
func healthProbeDB(t *testing.T, pingError error) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(healthProbeConnection{pingError: pingError})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{
		DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func registeredHealthExampleRouter(db *gorm.DB) http.Handler {
	router := chi.NewRouter()
	handlers.NewHealthHandler(db, nil).Register(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	return router
}

func TestHealthExamplesMatchRegisteredResponses(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"available", "database_connection_unavailable", "database_ping_failed", "internal_error"} {
		t.Run(name, func(t *testing.T) {
			pair, ok := pairs["getHealth"][name]
			examplepairs.Proven(t, "getHealth", name)
			if !ok {
				t.Fatal("the named health pair is missing")
			}
			rr := httptest.NewRecorder()
			var handler http.Handler
			switch name {
			case "available":
				handler = healthExampleRouter(t, nil)
			case "database_ping_failed":
				handler = healthExampleRouter(t, errors.New("private database dependency details"))
			case "database_connection_unavailable":
				// A GORM handle whose connection pool is unavailable follows the
				// other real health-handler refusal path, before any ping.
				handler = registeredHealthExampleRouter(&gorm.DB{Config: &gorm.Config{}})
			case "internal_error":
				endpoint.WriteError(rr, errors.New("private unexpected failure"))
			}
			if handler != nil {
				handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/health", nil))
			}
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("got %d %v: %s", rr.Code, rr.Header(), rr.Body.String())
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
		})
	}
	for _, warning := range describedRouters(t).generator("health examples").Report() {
		if warning.OperationID == "getHealth" {
			t.Errorf("health still has a generator warning: %v", warning)
		}
	}
}
