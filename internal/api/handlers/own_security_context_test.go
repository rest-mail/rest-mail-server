package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
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
	"github.com/restmail/restmail/internal/crypto"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The account's own security routes — the authenticator's status, enrollment,
// confirmation and removal, and the password change — run their SQL with the
// HTTP request's context (#1162, area #634). Each statement the driver sees is
// checked for the request's deadline and a value only the request carries, so
// a statement on a background context fails the test whatever it does.

type ownSecurityContextKey struct{}

// ownSecurityRow answers a statement, chosen by the first rule whose text it
// contains.
type ownSecurityRow struct {
	contains string
	columns  []string
	values   []driver.Value
	fail     bool
	wait     bool
}

type ownSecurityDB struct {
	serviceProbeTestDB
	rows    []ownSecurityRow
	mu      sync.Mutex
	seen    []string
	lost    []string
	commits int
	started chan struct{}
}

func (d *ownSecurityDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *ownSecurityDB) Driver() driver.Driver                        { return d }
func (d *ownSecurityDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *ownSecurityDB) answer(ctx context.Context, query string) (ownSecurityRow, error) {
	d.mu.Lock()
	d.seen = append(d.seen, query)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(ownSecurityContextKey{}) != true {
		d.lost = append(d.lost, query)
	}
	d.mu.Unlock()
	for _, row := range d.rows {
		if !strings.Contains(query, row.contains) {
			continue
		}
		switch {
		case row.wait:
			close(d.started)
			<-ctx.Done()
			return row, ctx.Err()
		case row.fail:
			return row, errors.New("private account store failure at 10.0.0.9")
		}
		return row, nil
	}
	return ownSecurityRow{}, nil
}

func (d *ownSecurityDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	row, err := d.answer(ctx, q)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(q, "INSERT") && row.columns == nil {
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(1)}}, nil
	}
	return &deliveryReadRows{columns: row.columns, values: row.values}, nil
}

func (d *ownSecurityDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if _, err := d.answer(ctx, q); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (d *ownSecurityDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if _, err := d.answer(ctx, "BEGIN"); err != nil {
		return nil, err
	}
	return &ownSecurityTx{d}, nil
}

type ownSecurityTx struct{ d *ownSecurityDB }

func (tx *ownSecurityTx) Commit() error {
	tx.d.mu.Lock()
	tx.d.commits++
	tx.d.mu.Unlock()
	return nil
}
func (tx *ownSecurityTx) Rollback() error { return nil }

func ownSecurityGorm(t *testing.T, d *ownSecurityDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func ownSecurityRouter(db *gorm.DB) http.Handler {
	router := chi.NewRouter()
	rs := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	noop := endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
	checks := SessionRouteChecks{Throttle: noop, Audit: func(string, ...middleware.AuditOption) endpoint.Middleware { return noop }}
	NewTwoFactorHandler(db, testMasterKey, true, nil).RegisterOwn(rs, checks)
	NewAuthHandler(db, auth.NewJWTService("own-security", time.Minute, time.Hour), testMasterKey, nil).
		RegisterPasswordChange(rs, checks)
	return router
}

func ownSecurityRequest(t *testing.T, method, path, body string) (*http.Request, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), ownSecurityContextKey{}, true), 4*time.Second)
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	return withClaims(r, mailboxClaims(7, "owner@example.test")), cancel
}

// totpCredentialRow is a stored authenticator, confirmed or pending, whose
// secret decrypts with the test key.
func totpCredentialRow(t *testing.T, secret string, confirmed bool) ([]string, []driver.Value) {
	t.Helper()
	encrypted, err := crypto.EncryptString(secret, testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	var confirmedAt driver.Value
	if confirmed {
		confirmedAt = time.Now().Add(-time.Hour)
	}
	return []string{"id", "account_id", "kind", "encrypted_secret", "confirmed_at"},
		[]driver.Value{int64(31), int64(7), "totp", encrypted, confirmedAt}
}

func TestOwnSecurityRoutesCarryTheRequestContext(t *testing.T) {
	key, err := auth.GenerateTOTPSecret(auth.DefaultTOTPIssuer, "owner@example.test")
	if err != nil {
		t.Fatal(err)
	}
	code := func() string { c, _ := auth.GenerateTOTPCode(key.Secret(), time.Now()); return c }
	pendingColumns, pending := totpCredentialRow(t, key.Secret(), false)
	activeColumns, active := totpCredentialRow(t, key.Secret(), true)
	hash, err := bcrypt.GenerateFromPassword([]byte("the-current-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	account := ownSecurityRow{contains: `FROM "mailboxes"`, columns: []string{"id", "address", "active", "two_factor_required"},
		values: []driver.Value{int64(7), "owner@example.test", true, false}}

	for _, tc := range []struct {
		name, method, path, body string
		rows                     []ownSecurityRow
		want                     int
		commits                  int
	}{
		{"status", http.MethodGet, "/api/v1/auth/2fa", "", nil, http.StatusOK, 0},
		{"enroll", http.MethodPost, "/api/v1/auth/2fa/enroll", "", nil, http.StatusOK, 1},
		{"confirm", http.MethodPost, "/api/v1/auth/2fa/confirm", `{"totp_code":"` + code() + `"}`,
			[]ownSecurityRow{{contains: `FROM "credentials"`, columns: pendingColumns, values: pending}}, http.StatusNoContent, 1},
		{"disable", http.MethodPost, "/api/v1/auth/2fa/disable", `{"totp_code":"` + code() + `"}`,
			[]ownSecurityRow{{contains: `FROM "credentials"`, columns: activeColumns, values: active}, account},
			http.StatusNoContent, 1},
		{"change password", http.MethodPost, "/api/v1/auth/password",
			`{"current_password":"the-current-password","new_password":"a-new-password-nobody-saw"}`,
			[]ownSecurityRow{account, {contains: `FROM "credentials"`, columns: []string{"id", "account_id", "kind", "secret_hash"},
				values: []driver.Value{int64(41), int64(7), "password", string(hash)}}},
			http.StatusOK, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &ownSecurityDB{rows: tc.rows}
			r, cancel := ownSecurityRequest(t, tc.method, tc.path, tc.body)
			defer cancel()
			rr := httptest.NewRecorder()
			ownSecurityRouter(ownSecurityGorm(t, d)).ServeHTTP(rr, r)
			if rr.Code != tc.want {
				t.Fatalf("status %d (%s), want %d", rr.Code, rr.Body.String(), tc.want)
			}
			if len(d.seen) == 0 {
				t.Fatal("no SQL reached the driver")
			}
			if len(d.lost) > 0 {
				t.Errorf("%d of %d statements ran without the request's context, first %q", len(d.lost), len(d.seen), d.lost[0])
			}
			if d.commits != tc.commits {
				t.Errorf("%d commits, want %d", d.commits, tc.commits)
			}
		})
	}
}

// A request cancelled while its SQL runs stops that SQL, and answers no success.
func TestOwnSecurityRoutesStopWhenTheRequestIsCancelled(t *testing.T) {
	for _, tc := range []struct{ name, method, path, body, waitOn string }{
		{"status", http.MethodGet, "/api/v1/auth/2fa", "", `FROM "credentials"`},
		{"change password", http.MethodPost, "/api/v1/auth/password",
			`{"current_password":"the-current-password","new_password":"a-new-password-nobody-saw"}`, `FROM "mailboxes"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &ownSecurityDB{rows: []ownSecurityRow{{contains: tc.waitOn, wait: true}}, started: make(chan struct{})}
			r, cancel := ownSecurityRequest(t, tc.method, tc.path, tc.body)
			go func() { <-d.started; cancel() }()
			rr := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { ownSecurityRouter(ownSecurityGorm(t, d)).ServeHTTP(rr, r); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("the SQL kept running after the request was cancelled")
			}
			if rr.Code < 400 {
				t.Errorf("a cancelled request answered %d", rr.Code)
			}
		})
	}
}

// A failed account read during a password change is the server's failure: a
// 401 would tell the client its session had ended.
func TestChangePasswordReportsAFailedAccountReadNotAnEndedSession(t *testing.T) {
	d := &ownSecurityDB{rows: []ownSecurityRow{{contains: `FROM "mailboxes"`, fail: true}}}
	r, cancel := ownSecurityRequest(t, http.MethodPost, "/api/v1/auth/password",
		`{"current_password":"the-current-password","new_password":"a-new-password-nobody-saw"}`)
	defer cancel()
	rr := httptest.NewRecorder()
	ownSecurityRouter(ownSecurityGorm(t, d)).ServeHTTP(rr, r)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d (%s), want 500", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "10.0.0.9") {
		t.Errorf("the dependency failure reached the client: %s", rr.Body.String())
	}

	// An account that is not there is still an ended session.
	gone := &ownSecurityDB{}
	r, cancel = ownSecurityRequest(t, http.MethodPost, "/api/v1/auth/password",
		`{"current_password":"the-current-password","new_password":"a-new-password-nobody-saw"}`)
	defer cancel()
	rr = httptest.NewRecorder()
	ownSecurityRouter(ownSecurityGorm(t, gone)).ServeHTTP(rr, r)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("a missing account: status %d (%s), want 401", rr.Code, rr.Body.String())
	}
}
