package handlers

import (
	"bytes"
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
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Every grant route runs its SQL — reads, the grant lock and the grant
// transactions — with the HTTP request's context (#1162, area #644). Only
// deleteGrant did; the rest ran on the background connection, so a client
// that went away left them running past the request's deadline.

type grantContextKey struct{}

type grantContextDB struct {
	serviceProbeTestDB
	mu         sync.Mutex
	seen, lost []string
}

func (d *grantContextDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *grantContextDB) Driver() driver.Driver                        { return d }
func (d *grantContextDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *grantContextDB) observe(ctx context.Context, q string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(grantContextKey{}) != true {
		d.lost = append(d.lost, q)
	}
}

func (d *grantContextDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.observe(ctx, q)
	if strings.HasPrefix(q, "SELECT count(") {
		return &deliveryReadRows{columns: []string{"count"}, values: []driver.Value{int64(0)}}, nil
	}
	return &deliveryReadRows{}, nil
}

func (d *grantContextDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.observe(ctx, q)
	return driver.RowsAffected(0), nil
}

func (d *grantContextDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.observe(ctx, "BEGIN")
	return ownerlessTx{}, nil
}

type ownerlessTx struct{}

func (ownerlessTx) Commit() error   { return nil }
func (ownerlessTx) Rollback() error { return nil }

func TestGrantRoutesCarryTheRequestContext(t *testing.T) {
	noop := endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
	for _, route := range []struct{ name, method, path, body string }{
		{"listRoles", http.MethodGet, "/api/v1/admin/roles", ""},
		{"listCapabilities", http.MethodGet, "/api/v1/admin/capabilities", ""},
		{"listGrants", http.MethodGet, "/api/v1/admin/grants", ""},
		{"listMyGrants", http.MethodGet, "/api/v1/admin/grants/mine", ""},
		{"createGrant", http.MethodPost, "/api/v1/admin/grants",
			`{"holder":{"kind":"account","id":3},"role":"admin","scope":"*","mode":"active"}`},
		{"activateGrant", http.MethodPost, "/api/v1/admin/grants/5/activate", `{"reason":"incident","totp_code":"123456"}`},
		{"deactivateGrant", http.MethodPost, "/api/v1/admin/grants/5/deactivate", ""},
		{"makeGrantEligible", http.MethodPost, "/api/v1/admin/grants/5/make-eligible", `{"totp_code":"123456"}`},
		{"deleteGrant", http.MethodDelete, "/api/v1/admin/grants/5", ""},
	} {
		t.Run(route.name, func(t *testing.T) {
			d := &grantContextDB{}
			pool := sql.OpenDB(d)
			t.Cleanup(func() { _ = pool.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
				&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			NewGrantHandler(db, grants.NewCache(db, time.Minute), testMasterKey).Register(
				endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()},
				GrantRouteChecks{At: func(string) endpoint.Middleware { return noop }, SessionOnly: noop, AuthThrottle: noop})

			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), grantContextKey{}, true), 4*time.Second)
			defer cancel()
			r := httptest.NewRequest(route.method, route.path, bytes.NewBufferString(route.body)).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			r = withClaims(r, &auth.Claims{AccountID: 7, Address: "admin@example.test",
				AMR: []string{auth.AMRPassword, auth.AMROTP}, TwoFactor: true})
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
