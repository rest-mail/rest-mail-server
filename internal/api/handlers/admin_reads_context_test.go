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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The admin routes left on the background connection — the ban list and
// removals, removing a DKIM key, the TLS report list, the dashboard and the
// pipeline analytics — run their SQL with the HTTP request's context (#1164).

type adminReadsContextKey struct{}

type adminReadsContextDB struct {
	serviceProbeTestDB
	mu         sync.Mutex
	seen, lost []string
}

func (d *adminReadsContextDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *adminReadsContextDB) Driver() driver.Driver                        { return d }
func (d *adminReadsContextDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *adminReadsContextDB) observe(ctx context.Context, q string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(adminReadsContextKey{}) != true {
		d.lost = append(d.lost, q)
	}
}

func (d *adminReadsContextDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.observe(ctx, q)
	if strings.HasPrefix(q, "SELECT count(") {
		return &deliveryReadRows{columns: []string{"count"}, values: []driver.Value{int64(0)}}, nil
	}
	return &deliveryReadRows{}, nil
}

func (d *adminReadsContextDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.observe(ctx, q)
	return driver.RowsAffected(1), nil
}

func (d *adminReadsContextDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.observe(ctx, "BEGIN")
	return ownerlessAdminTx{}, nil
}

type ownerlessAdminTx struct{}

func (ownerlessAdminTx) Commit() error   { return nil }
func (ownerlessAdminTx) Rollback() error { return nil }

func TestAdminReadRoutesCarryTheRequestContext(t *testing.T) {
	pass := func(string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
	}
	for _, route := range []struct{ name, method, path string }{
		{"listBans", http.MethodGet, "/api/v1/admin/bans"},
		{"deleteBan", http.MethodDelete, "/api/v1/admin/bans/3"},
		{"unbanIP", http.MethodDelete, "/api/v1/admin/bans/ip/192.0.2.7"},
		{"deleteDKIMKey", http.MethodDelete, "/api/v1/admin/dkim/3"},
		{"listTLSReports", http.MethodGet, "/api/v1/admin/tls-reports"},
		{"dashboardStats", http.MethodGet, "/api/v1/admin/stats"},
		{"getPipelineAnalytics", http.MethodGet, "/api/v1/admin/pipelines/analytics"},
	} {
		t.Run(route.name, func(t *testing.T) {
			d := &adminReadsContextDB{}
			pool := sql.OpenDB(d)
			t.Cleanup(func() { _ = pool.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
				&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			rs := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
			NewBanHandler(db).Register(rs, pass)
			NewDKIMHandler(db, testMasterKey).Register(rs, DKIMRouteChecks{At: pass, OnDomain: pass})
			NewTLSReportHandler(db).Register(rs, DomainPolicyRouteChecks{At: pass, OnDomain: pass})
			stats := NewStatsHandler(db)
			stats.Register(rs, pass)
			stats.RegisterPipelineAnalytics(rs, pass(""))

			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), adminReadsContextKey{}, true), 4*time.Second)
			defer cancel()
			r := withClaims(httptest.NewRequest(route.method, route.path, nil).WithContext(ctx), mailboxClaims(1, "admin@example.test"))
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, r)

			if len(d.seen) == 0 {
				t.Fatalf("no SQL reached the driver (status %d %s)", rr.Code, rr.Body.String())
			}
			if len(d.lost) > 0 {
				t.Errorf("%d of %d statements ran without the request's context (status %d), first %q",
					len(d.lost), len(d.seen), rr.Code, d.lost[0])
			}
		})
	}
}
