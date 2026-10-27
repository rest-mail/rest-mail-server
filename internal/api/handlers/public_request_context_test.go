package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type publicRequestContextKey struct{}

type publicContextDB struct {
	serviceProbeTestDB
	wait      bool
	started   chan struct{}
	calls     atomic.Int32
	lost      atomic.Int32
	cancelled atomic.Int32
}

func (c *publicContextDB) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *publicContextDB) Driver() driver.Driver                        { return c }
func (c *publicContextDB) Open(string) (driver.Conn, error)             { return c, nil }

func (c *publicContextDB) observe(ctx context.Context) error {
	c.calls.Add(1)
	if _, deadline := ctx.Deadline(); !deadline || ctx.Value(publicRequestContextKey{}) != true {
		c.lost.Add(1)
	}
	if c.wait {
		select {
		case c.started <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			c.cancelled.Add(1)
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return errors.New("finite database backstop: HTTP cancellation was lost")
		}
	}
	return ctx.Err()
}

func (c *publicContextDB) Ping(ctx context.Context) error { return c.observe(ctx) }

func (c *publicContextDB) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := c.observe(ctx); err != nil {
		return nil, err
	}
	return &publicDomainRows{}, nil
}

type publicDomainRows struct{ done bool }

func (*publicDomainRows) Columns() []string { return []string{"id"} }
func (*publicDomainRows) Close() error      { return nil }
func (r *publicDomainRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	values[0] = int64(1)
	return nil
}

func publicContextGorm(t *testing.T, c *publicContextDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(c)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

var publicContextRoutes = []struct {
	name, method, path string
	handler            func(*gorm.DB) http.Handler
	failureStatus      int
}{
	{"health", http.MethodGet, "/api/health", func(db *gorm.DB) http.Handler { return healthRoutes(NewHealthHandler(db, nil)) }, http.StatusServiceUnavailable},
	{"mozilla", http.MethodGet, "/mail/config-v1.1.xml?emailaddress=user@example.test", publicAutoconfigRoutes, http.StatusInternalServerError},
	{"mozilla well-known", http.MethodGet, "/.well-known/autoconfig/mail/config-v1.1.xml", publicAutoconfigRoutes, http.StatusInternalServerError},
	{"microsoft", http.MethodPost, "/autodiscover/autodiscover.xml?emailaddress=user@example.test", publicAutoconfigRoutes, http.StatusInternalServerError},
}

func publicAutoconfigRoutes(db *gorm.DB) http.Handler {
	return autoconfigRoutes(NewAutoconfigHandler(db, "mail.example.test"))
}

// The SQL driver sees the actual context passed by database/sql, not just an
// ORM callback. Both a deadline and an HTTP-scoped value must reach it.
func TestPublicDiscoveryAndHealthCarryHTTPContext(t *testing.T) {
	for _, route := range publicContextRoutes {
		t.Run(route.name, func(t *testing.T) {
			c := &publicContextDB{}
			handler := route.handler(publicContextGorm(t, c))
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), publicRequestContextKey{}, true), time.Second)
			defer cancel()
			r := httptest.NewRequest(route.method, route.path, nil).WithContext(ctx)
			r.Host = "example.test"
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, r)
			if rr.Code != http.StatusOK || c.calls.Load() != 1 || c.lost.Load() != 0 {
				t.Fatalf("status%d database calls%d lost HTTP contexts%d", rr.Code, c.calls.Load(), c.lost.Load())
			}
		})
	}
}

func TestPublicDiscoveryAndHealthCancelBlockedDatabaseWork(t *testing.T) {
	for _, route := range publicContextRoutes {
		t.Run(route.name, func(t *testing.T) {
			c := &publicContextDB{wait: true, started: make(chan struct{}, 1)}
			handler := route.handler(publicContextGorm(t, c))
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), publicRequestContextKey{}, true), 5*time.Second)
			defer cancel()
			r := httptest.NewRequest(route.method, route.path, nil).WithContext(ctx)
			r.Host = "example.test"
			rr := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				handler.ServeHTTP(rr, r)
			}()
			select {
			case <-c.started:
				cancel()
			case <-time.After(time.Second):
				cancel()
				t.Error("database operation never started")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled request did not return before the finite database backstop")
			}
			if rr.Code != route.failureStatus || c.calls.Load() != 1 || c.lost.Load() != 0 || c.cancelled.Load() != 1 {
				t.Fatalf("status%d calls%d lost contexts%d cancelled database calls%d", rr.Code, c.calls.Load(), c.lost.Load(), c.cancelled.Load())
			}
		})
	}
}
