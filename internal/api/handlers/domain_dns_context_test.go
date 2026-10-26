package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type domainDNSContextKey struct{}
type domainDNSContextDB struct{ serviceProbeTestDB }

func (c *domainDNSContextDB) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *domainDNSContextDB) Driver() driver.Driver                        { return c }
func (c *domainDNSContextDB) Open(string) (driver.Conn, error)             { return c, nil }
func (c *domainDNSContextDB) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	if ctx.Value(domainDNSContextKey{}) != true {
		return nil, errors.New("domain lookup lost HTTP context")
	}
	return &domainDNSContextRows{}, nil
}

type domainDNSContextRows struct{ done bool }

func (*domainDNSContextRows) Columns() []string {
	return []string{"id", "name", "dkim_selector", "dkim_private_key"}
}
func (*domainDNSContextRows) Close() error { return nil }
func (r *domainDNSContextRows) Next(v []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	v[0] = int64(1)
	v[1] = "example.com"
	v[2] = "rm2026"
	v[3] = "public-test-marker"
	return nil
}

// The resolver refuses immediately, so even the old background-context path
// cannot make an external request or hang while demonstrating the lost context.
func TestDomainDNSCheckCarriesHTTPContext(t *testing.T) {
	var calls, bad atomic.Int32
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		calls.Add(1)
		_, deadline := ctx.Deadline()
		if ctx.Value(domainDNSContextKey{}) != true || !deadline {
			bad.Add(1)
		}
		return nil, errors.New("controlled DNS refusal")
	}}
	defer func() { net.DefaultResolver = previous }()
	c := &domainDNSContextDB{}
	pool := sql.OpenDB(c)
	defer pool.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	h := NewDomainHandler(db, nil)
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), domainDNSContextKey{}, true), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/domains/1/dns", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	domainRoutes(h).ServeHTTP(rr, req)
	if rr.Code != 200 || calls.Load() == 0 || bad.Load() != 0 {
		t.Fatalf("status%d DNS calls%d lost HTTP context/deadline%d", rr.Code, calls.Load(), bad.Load())
	}
}

// A blocked DNS exchange must unblock when HTTP is cancelled. A separate bounded
// backstop makes this regression finite even if a background context returns.
func TestDomainDNSCheckCancelsBlockedResolver(t *testing.T) {
	var calls, bad atomic.Int32
	started := make(chan struct{}, 1)
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			bad.Add(1)
			return nil, errors.New("DNS backstop expired")
		}
	}}
	defer func() { net.DefaultResolver = previous }()
	pool := sql.OpenDB(&domainDNSContextDB{})
	defer pool.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	h := NewDomainHandler(db, nil)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), domainDNSContextKey{}, true))
	defer cancel()
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		domainRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/admin/domains/1/dns", nil).WithContext(ctx))
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		cancel()
		t.Error("DNS exchange never started")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled DNS request did not return before its finite backstop")
	}
	if rr.Code != 200 || calls.Load() == 0 || bad.Load() != 0 || ctx.Err() != context.Canceled {
		t.Fatalf("status%d calls%d background fallbacks%d context%v", rr.Code, calls.Load(), bad.Load(), ctx.Err())
	}
}
