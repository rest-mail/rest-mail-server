package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The account's quarantine list and delete, and the test endpoint that polls
// for a delivered message, run their SQL with the HTTP request's context
// (#1163, #1167). The poll ran for up to 30 seconds whether or not anyone was
// still waiting for its answer.

type quarantineVerifyKey struct{}

type quarantineVerifyDB struct {
	serviceProbeTestDB
	mu         sync.Mutex
	seen, lost []string
}

func (d *quarantineVerifyDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *quarantineVerifyDB) Driver() driver.Driver                        { return d }
func (d *quarantineVerifyDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *quarantineVerifyDB) observe(ctx context.Context, q string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(quarantineVerifyKey{}) != true {
		d.lost = append(d.lost, q)
	}
}

func (d *quarantineVerifyDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.observe(ctx, q)
	switch {
	case strings.HasPrefix(q, "SELECT count("):
		return &deliveryReadRows{columns: []string{"count"}, values: []driver.Value{int64(0)}}, nil
	case strings.Contains(q, `FROM "mailboxes"`):
		return &deliveryReadRows{columns: []string{"id", "address", "active"},
			values: []driver.Value{int64(7), "owner@example.test", true}}, nil
	}
	return &deliveryReadRows{}, nil
}

func (d *quarantineVerifyDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.observe(ctx, q)
	return driver.RowsAffected(1), nil
}

func (d *quarantineVerifyDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.observe(ctx, "BEGIN")
	return ownerlessQuarantineTx{}, nil
}

type ownerlessQuarantineTx struct{}

func (ownerlessQuarantineTx) Commit() error   { return nil }
func (ownerlessQuarantineTx) Rollback() error { return nil }

func quarantineVerifyRouter(t *testing.T, d *quarantineVerifyDB) http.Handler {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	rs := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	NewPipelineHandler(db, nil, filters.JSSidecar{}).RegisterQuarantine(rs)
	pass := func(string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
	}
	NewTestHandler(db, &config.Config{}).Register(rs, TestRouteChecks{At: pass})
	return router
}

func TestQuarantineRoutesCarryTheRequestContext(t *testing.T) {
	for _, route := range []struct{ name, method, path string }{
		{"listQuarantine", http.MethodGet, "/api/v1/accounts/7/quarantine"},
		{"deleteQuarantine", http.MethodDelete, "/api/v1/accounts/7/quarantine/5"},
	} {
		t.Run(route.name, func(t *testing.T) {
			d := &quarantineVerifyDB{}
			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), quarantineVerifyKey{}, true), 4*time.Second)
			defer cancel()
			r := withClaims(httptest.NewRequest(route.method, route.path, nil).WithContext(ctx),
				&auth.Claims{AccountID: 7, Address: "owner@example.test", CredentialID: 3, CredentialKind: "app_password"})
			rr := httptest.NewRecorder()
			quarantineVerifyRouter(t, d).ServeHTTP(rr, r)
			if len(d.seen) < 2 {
				t.Fatalf("only %d statements reached the driver (status %d %s)", len(d.seen), rr.Code, rr.Body.String())
			}
			if len(d.lost) > 0 {
				t.Errorf("%d of %d statements ran without the request's context (status %d), first %q",
					len(d.lost), len(d.seen), rr.Code, d.lost[0])
			}
		})
	}
}

// A caller that goes away stops the delivery poll.
func TestVerifyDeliveryStopsWhenTheCallerGoesAway(t *testing.T) {
	d := &quarantineVerifyDB{}
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), quarantineVerifyKey{}, true), 4*time.Second)
	r := withClaims(httptest.NewRequest(http.MethodGet,
		"/api/v1/admin/test/verify?to=owner@example.test&subject=hello&timeout=30s", nil).WithContext(ctx),
		mailboxClaims(1, "admin@example.test"))
	time.AfterFunc(300*time.Millisecond, cancel)

	started := time.Now()
	rr := httptest.NewRecorder()
	quarantineVerifyRouter(t, d).ServeHTTP(rr, r)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("the poll ran %s after the caller went away at 300ms", elapsed)
	}
	if rr.Code == http.StatusOK {
		t.Errorf("a cancelled poll answered 200 %s", rr.Body.String())
	}
	if len(d.lost) > 0 {
		t.Errorf("%d of %d statements ran without the request's context, first %q", len(d.lost), len(d.seen), d.lost[0])
	}
}
