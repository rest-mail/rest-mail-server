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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The account's Sieve script and vacation reply routes run their SQL — the
// check that the caller may manage the account's mail rules first — with the
// HTTP request's context (#1163). The access check, and every read of the
// read routes, ran on the background connection.

type mailRulesContextKey struct{}

type mailRulesContextDB struct {
	serviceProbeTestDB
	mu         sync.Mutex
	seen, lost []string
}

func (d *mailRulesContextDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *mailRulesContextDB) Driver() driver.Driver                        { return d }
func (d *mailRulesContextDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *mailRulesContextDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(mailRulesContextKey{}) != true {
		d.lost = append(d.lost, q)
	}
	return &deliveryReadRows{}, nil
}

func TestMailRulesRoutesCarryTheRequestContext(t *testing.T) {
	for _, route := range []struct{ name, method, path, body string }{
		{"getSieveScript", http.MethodGet, "/api/v1/accounts/7/sieve", ""},
		{"putSieveScript", http.MethodPut, "/api/v1/accounts/7/sieve", `{"script":"keep;"}`},
		{"deleteSieveScript", http.MethodDelete, "/api/v1/accounts/7/sieve", ""},
		{"validateSieveScript", http.MethodPost, "/api/v1/accounts/7/sieve/validate", `{"script":"keep;"}`},
		{"getVacation", http.MethodGet, "/api/v1/accounts/7/vacation", ""},
		{"setVacation", http.MethodPut, "/api/v1/accounts/7/vacation", `{"enabled":true,"subject":"Away","body":"Back soon"}`},
		{"disableVacation", http.MethodDelete, "/api/v1/accounts/7/vacation", ""},
	} {
		t.Run(route.name, func(t *testing.T) {
			d := &mailRulesContextDB{}
			pool := sql.OpenDB(d)
			t.Cleanup(func() { _ = pool.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
				&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			rs := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
			NewSieveHandler(db).Register(rs)
			NewVacationHandler(db).Register(rs)

			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), mailRulesContextKey{}, true), 4*time.Second)
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
