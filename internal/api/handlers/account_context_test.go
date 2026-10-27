package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Listing, reading, creating and linking accounts run their SQL with the
// HTTP request's context (#1162, area #635). Only unlinkAccount did; the
// others ran on the background connection, so a client that went away left
// the reads, the password check and the account transaction running past
// the request's deadline.

type accountContextKey struct{}

type accountContextDB struct {
	serviceProbeTestDB
	mu         sync.Mutex
	seen, lost []string
}

func (d *accountContextDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *accountContextDB) Driver() driver.Driver                        { return d }
func (d *accountContextDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *accountContextDB) observe(ctx context.Context, q string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(accountContextKey{}) != true {
		d.lost = append(d.lost, q)
	}
}

func (d *accountContextDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.observe(ctx, q)
	return &deliveryReadRows{}, nil
}

func (d *accountContextDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.observe(ctx, q)
	return driver.RowsAffected(0), nil
}

func TestAccountRoutesCarryTheRequestContext(t *testing.T) {
	noop := endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
	for _, route := range []struct{ name, method, path, body string }{
		{"listAccounts", http.MethodGet, "/api/v1/accounts", ""},
		{"getAccount", http.MethodGet, "/api/v1/accounts/9", ""},
		{"createAccount", http.MethodPost, "/api/v1/accounts", `{"address":"new@example.test"}`},
		{"linkAccount", http.MethodPost, "/api/v1/accounts/link", `{"address":"other@example.test","password":"a-password-nobody-has"}`},
	} {
		t.Run(route.name, func(t *testing.T) {
			d := &accountContextDB{}
			pool := sql.OpenDB(d)
			t.Cleanup(func() { _ = pool.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
				&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			rs := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
			h := NewAccountHandler(db, grants.NewCache(db, time.Minute), testMasterKey)
			h.RegisterReads(rs)
			h.RegisterCreate(rs, noop)
			h.RegisterLinks(rs, SessionRouteChecks{Throttle: noop,
				Audit: func(string, ...middleware.AuditOption) endpoint.Middleware { return noop }})

			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), accountContextKey{}, true), 4*time.Second)
			defer cancel()
			r := httptest.NewRequest(route.method, route.path, bytes.NewBufferString(route.body)).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			r = withClaims(r, mailboxClaims(7, "owner@example.test"))
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
