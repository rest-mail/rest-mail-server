package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type liveAccountContextKey struct{}
type liveAccountDB struct {
	serviceProbeTestDB
	missing, wait, aliveFirst, accountFirst               bool
	queries, executions, begins, commits, rollbacks, lost atomic.Int32
	started                                               chan struct{}
}

func (d *liveAccountDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *liveAccountDB) Driver() driver.Driver                        { return d }
func (d *liveAccountDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *liveAccountDB) observe(ctx context.Context) {
	if _, ok := ctx.Deadline(); !ok || ctx.Value(liveAccountContextKey{}) != true {
		d.lost.Add(1)
	}
}
func (d *liveAccountDB) failure(ctx context.Context) error {
	d.observe(ctx)
	if d.wait {
		close(d.started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return errors.New("HTTP cancellation lost")
		}
	}
	return errors.New("private identity dependency failure")
}
func (d *liveAccountDB) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	d.queries.Add(1)
	if d.accountFirst && d.queries.Load() == 1 {
		d.observe(ctx)
		return &liveAccountRows{columns: []string{"id", "address"}, values: []driver.Value{int64(2), "linked@example.test"}}, nil
	}
	if d.aliveFirst && d.queries.Load() == 1 {
		d.observe(ctx)
		if !strings.Contains(query, `FROM "refresh_tokens"`) {
			return nil, errors.New("unexpected session lookup")
		}
		return &liveAccountRows{columns: []string{"count"}, values: []driver.Value{int64(1)}}, nil
	}
	if d.missing {
		d.observe(ctx)
		return &liveAccountRows{columns: []string{"id"}}, nil
	}
	return nil, d.failure(ctx)
}
func (d *liveAccountDB) ExecContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	d.executions.Add(1)
	return nil, d.failure(ctx)
}
func (d *liveAccountDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.observe(ctx)
	d.begins.Add(1)
	return &liveAccountTx{d}, nil
}

type liveAccountTx struct{ d *liveAccountDB }

func (tx *liveAccountTx) Commit() error   { tx.d.commits.Add(1); return nil }
func (tx *liveAccountTx) Rollback() error { tx.d.rollbacks.Add(1); return nil }

type liveAccountRows struct {
	columns []string
	values  []driver.Value
	done    bool
}

func (r *liveAccountRows) Columns() []string { return r.columns }
func (r *liveAccountRows) Close() error      { return nil }
func (r *liveAccountRows) Next(out []driver.Value) error {
	if r.done || r.values == nil {
		return io.EOF
	}
	r.done = true
	copy(out, r.values)
	return nil
}
func liveAccountGorm(t *testing.T, d *liveAccountDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

var liveAccountRoutes = []struct{ name, method, path, body string }{
	{"events", http.MethodGet, "/api/v1/accounts/2/events", ""},
	{"unlink", http.MethodDelete, "/api/v1/accounts/2", ""},
	{"device list", http.MethodGet, "/api/v1/devices", ""},
	{"device register", http.MethodPost, "/api/v1/devices", `{"platform":"apns","token":"` + strings.Repeat("ab", 32) + `"}`},
	{"device remove", http.MethodDelete, "/api/v1/devices/2", ""},
}

func liveAccountRouter(db *gorm.DB) (http.Handler, *SSEBroker) {
	router := chi.NewRouter()
	rs := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	noop := endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	audit := func(string, ...middleware.AuditOption) endpoint.Middleware { return noop }
	NewDeviceHandler(db, pushTestTopic).RegisterRoutes(rs, audit)
	NewAccountHandler(db, nil, "").RegisterLinks(rs, SessionRouteChecks{Throttle: noop, Audit: audit})
	broker := newSSEBroker(1)
	NewEventHandler(db, broker, func(uint, int) (bool, error) { return true, nil }).Register(rs)
	return router, broker
}
func liveAccountRequest(t *testing.T, index int) *http.Request {
	t.Helper()
	route := liveAccountRoutes[index]
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), liveAccountContextKey{}, true), 3*time.Second)
	t.Cleanup(cancel)
	claims := freshTwoFactorClaims(1, "")
	claims.SessionID = "session-one"
	ctx = context.WithValue(ctx, middleware.ClaimsKey, claims)
	r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	return r
}
func assertLiveAccountFault(t *testing.T, d *liveAccountDB, broker *SSEBroker, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != 500 || !strings.Contains(rr.Body.String(), `"code":"internal_error"`) || strings.Contains(rr.Body.String(), "private") || strings.Contains(rr.Header().Get("Content-Type"), "text/event-stream") || d.lost.Load() != 0 || d.commits.Load() != 0 || len(broker.active) != 0 || len(broker.subscribers) != 0 {
		t.Fatalf("status=%d body=%s SQL queries=%d executions=%d begins=%d commits=%d rollbacks=%d lost contexts=%d streams=%v", rr.Code, rr.Body, d.queries.Load(), d.executions.Load(), d.begins.Load(), d.commits.Load(), d.rollbacks.Load(), d.lost.Load(), broker.active)
	}
}
func TestLiveAccountDependencyFailuresRetainInternalErrors(t *testing.T) {
	for index, route := range liveAccountRoutes {
		t.Run(route.name, func(t *testing.T) {
			d := &liveAccountDB{}
			h, broker := liveAccountRouter(liveAccountGorm(t, d))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, liveAccountRequest(t, index))
			assertLiveAccountFault(t, d, broker, rr)
			if index == 4 {
				if d.executions.Load() != 1 || d.begins.Load() != 1 || d.rollbacks.Load() != 1 {
					t.Fatal("failed device removal was not rolled back")
				}
			} else if d.queries.Load() != 1 || d.begins.Load() != 0 {
				t.Fatal("failed lookup continued to mutate or read")
			}
		})
	}
}
func TestLiveAccountCancellationReachesSQL(t *testing.T) {
	for index, route := range liveAccountRoutes {
		t.Run(route.name, func(t *testing.T) {
			d := &liveAccountDB{wait: true, started: make(chan struct{})}
			h, broker := liveAccountRouter(liveAccountGorm(t, d))
			rr := httptest.NewRecorder()
			r := liveAccountRequest(t, index)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); h.ServeHTTP(rr, r.WithContext(ctx)) }()
			select {
			case <-d.started:
				cancel()
			case <-time.After(time.Second):
				t.Error("SQL never started")
				cancel()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("handler ignored cancellation")
			}
			assertLiveAccountFault(t, d, broker, rr)
		})
	}
}
func TestLiveAccountMissingResponsesKeepTheirPolicy(t *testing.T) {
	for _, index := range []int{0, 1, 2} {
		t.Run(liveAccountRoutes[index].name, func(t *testing.T) {
			d := &liveAccountDB{missing: true}
			h, broker := liveAccountRouter(liveAccountGorm(t, d))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, liveAccountRequest(t, index))
			want := []int{403, 404, 200}[index]
			if rr.Code != want || d.queries.Load() != 1 || d.lost.Load() != 0 || d.begins.Load() != 0 || len(broker.active) != 0 {
				t.Fatalf("status=%d body=%s queries=%d lost=%d", rr.Code, rr.Body, d.queries.Load(), d.lost.Load())
			}
			if index == 2 && (strings.Contains(rr.Body.String(), `"token"`) || !strings.Contains(rr.Body.String(), `"devices":[]`)) {
				t.Fatal("empty session device response changed")
			}
		})
	}
}
func TestDeviceRegistrationTransactionRetainsHTTPContext(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(map[bool]string{false: "query fault", true: "cancelled query"}[wait], func(t *testing.T) {
			d := &liveAccountDB{aliveFirst: true, wait: wait, started: make(chan struct{})}
			h, broker := liveAccountRouter(liveAccountGorm(t, d))
			rr := httptest.NewRecorder()
			r := liveAccountRequest(t, 3)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			if wait {
				done := make(chan struct{})
				go func() { defer close(done); h.ServeHTTP(rr, r.WithContext(ctx)) }()
				select {
				case <-d.started:
					cancel()
				case <-time.After(time.Second):
					t.Error("registration SQL never started")
					cancel()
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("registration ignored cancellation")
				}
			} else {
				h.ServeHTTP(rr, r)
			}
			assertLiveAccountFault(t, d, broker, rr)
			if d.queries.Load() != 2 || d.begins.Load() != 1 {
				t.Fatal("registration never reached its transactional device lookup")
			}
		})
	}
}

func TestAccountUnlinkTransactionRetainsHTTPContext(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(map[bool]string{false: "grant lock fault", true: "cancelled grant lock"}[wait], func(t *testing.T) {
			d := &liveAccountDB{accountFirst: true, wait: wait, started: make(chan struct{})}
			h, broker := liveAccountRouter(liveAccountGorm(t, d))
			rr := httptest.NewRecorder()
			r := liveAccountRequest(t, 1)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			if wait {
				done := make(chan struct{})
				go func() { defer close(done); h.ServeHTTP(rr, r.WithContext(ctx)) }()
				select {
				case <-d.started:
					cancel()
				case <-time.After(time.Second):
					t.Error("grant lock never started")
					cancel()
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("grant lock ignored cancellation")
				}
			} else {
				h.ServeHTTP(rr, r)
			}
			assertLiveAccountFault(t, d, broker, rr)
			if d.queries.Load() != 1 || d.executions.Load() != 1 || d.begins.Load() != 1 {
				t.Fatal("unlink never reached the grant-removal transaction")
			}
		})
	}
}
