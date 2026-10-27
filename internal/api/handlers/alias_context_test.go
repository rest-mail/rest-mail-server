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

// Listing, reading and deleting aliases run their SQL with the HTTP request's
// context (#1164), as creating and updating one already did.

type aliasContextKey struct{}

type aliasContextDB struct {
	serviceProbeTestDB
	mu         sync.Mutex
	seen, lost []string
}

func (d *aliasContextDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *aliasContextDB) Driver() driver.Driver                        { return d }
func (d *aliasContextDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *aliasContextDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(aliasContextKey{}) != true {
		d.lost = append(d.lost, q)
	}
	if strings.HasPrefix(q, "SELECT count(") {
		return &deliveryReadRows{columns: []string{"count"}, values: []driver.Value{int64(0)}}, nil
	}
	return &deliveryReadRows{}, nil
}

func TestAliasRoutesCarryTheRequestContext(t *testing.T) {
	noop := func(string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
	}
	for _, route := range []struct{ name, method, path string }{
		{"listAliases", http.MethodGet, "/api/v1/admin/aliases"},
		{"getAlias", http.MethodGet, "/api/v1/admin/aliases/4"},
		{"deleteAlias", http.MethodDelete, "/api/v1/admin/aliases/4"},
	} {
		t.Run(route.name, func(t *testing.T) {
			d := &aliasContextDB{}
			pool := sql.OpenDB(d)
			t.Cleanup(func() { _ = pool.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
				&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			NewAliasHandler(db).Register(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()},
				AliasRouteChecks{OnList: noop, OnAlias: noop})

			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), aliasContextKey{}, true), 4*time.Second)
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
