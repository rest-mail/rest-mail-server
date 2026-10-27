package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Signing in, out and back in — login, logout, refresh, a reset token and
// self-service recovery — run their SQL with the HTTP request's context
// (#1162, areas #632 and #633). They ran on the background connection, so a
// client that went away left the password check, the ledger writes and the
// reset transaction running past the request's deadline.

type signinContextKey struct{}

type signinContextDB struct {
	serviceProbeTestDB
	mu         sync.Mutex
	seen, lost []string
}

func (d *signinContextDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *signinContextDB) Driver() driver.Driver                        { return d }
func (d *signinContextDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *signinContextDB) observe(ctx context.Context, q string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(signinContextKey{}) != true {
		d.lost = append(d.lost, q)
	}
}

func (d *signinContextDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.observe(ctx, q)
	return &deliveryReadRows{}, nil
}

func (d *signinContextDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.observe(ctx, q)
	return driver.RowsAffected(0), nil
}

func (d *signinContextDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.observe(ctx, "BEGIN")
	return ownerlessSigninTx{}, nil
}

type ownerlessSigninTx struct{}

func (ownerlessSigninTx) Commit() error   { return nil }
func (ownerlessSigninTx) Rollback() error { return nil }

func TestSignInRoutesCarryTheRequestContext(t *testing.T) {
	jwt := auth.NewJWTService("signin-context-secret", 15*time.Minute, 7*24*time.Hour)
	pair, err := jwt.GenerateSession(7, "someone@example.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded := func(body map[string]string) string {
		out, _ := json.Marshal(body)
		return string(out)
	}
	native := func(body map[string]string) string {
		body["client"] = "native"
		return encoded(body)
	}
	for _, route := range []struct{ name, path, body string }{
		{"login", "/api/v1/auth/login", native(map[string]string{"email": "someone@example.test", "password": "a-password-nobody-has"})},
		{"logout", "/api/v1/auth/logout", encoded(map[string]string{"refresh_token": pair.RefreshToken})},
		{"refreshToken", "/api/v1/auth/refresh", encoded(map[string]string{"refresh_token": pair.RefreshToken})},
		{"resetAdmin", "/api/v1/auth/reset", native(map[string]string{"token": "not-a-token-anyone-issued", "password": "a-long-enough-password"})},
		{"recoverAccount", "/api/v1/auth/recover", native(map[string]string{"email": "someone@example.test",
			"totp_code": "123456", "recovery_code": "not-a-recovery-code", "new_password": "a-long-enough-password"})},
	} {
		t.Run(route.name, func(t *testing.T) {
			d := &signinContextDB{}
			pool := sql.OpenDB(d)
			t.Cleanup(func() { _ = pool.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
				&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			noop := endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
			NewAuthHandler(db, jwt, testMasterKey, nil).RegisterSignIn(
				endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()},
				SessionRouteChecks{Throttle: noop, Audit: func(string, ...middleware.AuditOption) endpoint.Middleware { return noop }})

			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), signinContextKey{}, true), 4*time.Second)
			defer cancel()
			r := httptest.NewRequest(http.MethodPost, route.path, bytes.NewBufferString(route.body)).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, r)

			if len(d.seen) == 0 {
				t.Fatalf("no SQL reached the driver (status %d %s)", rr.Code, rr.Body.String())
			}
			if len(d.lost) > 0 {
				t.Errorf("%d of %d statements ran without the request's context (status %d), first %q",
					len(d.lost), len(d.seen), rr.Code, strings.TrimSpace(d.lost[0]))
			}
		})
	}
}
