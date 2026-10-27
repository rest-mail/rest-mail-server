package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// Script actual database/sql calls, including transaction boundaries. No callback
// replaces the handler, repository, grant checks or SQL context propagation.
type credentialSQLStep struct {
	sql        string
	columns    []string
	values     []driver.Value
	affected   int64
	fail, wait bool
}
type credentialContextDB struct {
	serviceProbeTestDB
	steps                                   []credentialSQLStep
	calls, lost, begins, commits, rollbacks atomic.Int32
	started                                 chan struct{}
}
type credentialContextKey struct{}

func (d *credentialContextDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *credentialContextDB) Driver() driver.Driver                        { return d }
func (d *credentialContextDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *credentialContextDB) observe(ctx context.Context) {
	if _, ok := ctx.Deadline(); !ok || ctx.Value(credentialContextKey{}) != true {
		d.lost.Add(1)
	}
}
func (d *credentialContextDB) step(ctx context.Context, query string) (credentialSQLStep, error) {
	d.observe(ctx)
	i := int(d.calls.Add(1)) - 1
	if i >= len(d.steps) {
		return credentialSQLStep{}, errors.New("unexpected credential SQL")
	}
	s := d.steps[i]
	if !strings.Contains(query, s.sql) {
		return s, errors.New("unexpected credential statement: " + query)
	}
	if s.wait {
		close(d.started)
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-time.After(2 * time.Second):
			return s, errors.New("private lost HTTP cancellation")
		}
	}
	if s.fail {
		return s, errors.New("private credential dependency failure")
	}
	return s, nil
}
func (d *credentialContextDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	s, err := d.step(ctx, q)
	if err != nil {
		return nil, err
	}
	return &deliveryReadRows{columns: s.columns, values: s.values}, nil
}
func (d *credentialContextDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "ROLLBACK TO SAVEPOINT") {
		d.observe(ctx)
		return driver.RowsAffected(0), nil
	}
	s, err := d.step(ctx, q)
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(s.affected), nil
}
func (d *credentialContextDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.observe(ctx)
	d.begins.Add(1)
	return &credentialContextTx{d}, nil
}

type credentialContextTx struct{ d *credentialContextDB }

func (tx *credentialContextTx) Commit() error   { tx.d.commits.Add(1); return nil }
func (tx *credentialContextTx) Rollback() error { tx.d.rollbacks.Add(1); return nil }
func credentialContextGorm(t *testing.T, d *credentialContextDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

var credentialContextRoutes = []struct{ name, method, path, body string }{
	{"app list", "GET", "/api/v1/accounts/7/credentials", ""},
	{"key list", "GET", "/api/v1/accounts/7/api-keys", ""},
	{"app create", "POST", "/api/v1/accounts/7/credentials", `{"name":"phone","password":"fixture-password"}`},
	{"key create", "POST", "/api/v1/accounts/7/api-keys", `{"name":"phone","password":"fixture-password","permissions":[{"capability":"mail:read","scope":"mailbox:reader@example.test"}]}`},
	{"app revoke", "DELETE", "/api/v1/accounts/7/credentials/21", ""},
	{"key revoke", "DELETE", "/api/v1/accounts/7/api-keys/21", ""},
	{"protocol sign-in", "POST", "/api/v1/auth/protocol-sign-in", `{"protocol":"imap"}`},
}

func credentialContextRouter(h *CredentialHandler) http.Handler {
	router := chi.NewRouter()
	rs := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	noop := endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
	checks := SessionRouteChecks{Throttle: noop, Audit: func(string, ...middleware.AuditOption) endpoint.Middleware { return noop }}
	h.Register(rs, checks)
	h.RegisterProtocolSignIn(rs, checks)
	return router
}
func credentialContextRequest(t *testing.T, index int) *http.Request {
	t.Helper()
	route := credentialContextRoutes[index]
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), credentialContextKey{}, true), 4*time.Second)
	t.Cleanup(cancel)
	claims := &auth.Claims{AccountID: 7, Address: "reader@example.test", TwoFactor: true}
	if index == 6 {
		claims.CredentialID = 11
		claims.CredentialKind = "app_password"
	}
	ctx = context.WithValue(ctx, middleware.ClaimsKey, claims)
	r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	return r
}
func credentialAccessSteps() []credentialSQLStep {
	return []credentialSQLStep{
		{sql: `FROM "mailboxes"`, columns: []string{"id", "address", "active"}, values: []driver.Value{int64(7), "reader@example.test", true}},
		{sql: `FROM grants AS g`, columns: []string{"scope", "capability"}},
		{sql: `FROM "mailboxes"`, columns: []string{"address"}, values: []driver.Value{"reader@example.test"}},
	}
}
func assertCredentialContextFault(t *testing.T, d *credentialContextDB, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != 500 || !strings.Contains(rr.Body.String(), `"code":"internal_error"`) || strings.Contains(rr.Body.String(), "private") || strings.Contains(rr.Body.String(), "fixture-password") || d.lost.Load() != 0 || d.commits.Load() != 0 || int(d.calls.Load()) != len(d.steps) {
		t.Fatalf("status=%d body=%s calls=%d/%d lost=%d begins=%d commits=%d rollbacks=%d", rr.Code, rr.Body, d.calls.Load(), len(d.steps), d.lost.Load(), d.begins.Load(), d.commits.Load(), d.rollbacks.Load())
	}
}
func TestCredentialRoutesCarryRequestContext(t *testing.T) {
	for index, route := range credentialContextRoutes {
		t.Run(route.name, func(t *testing.T) {
			steps := credentialAccessSteps()
			if index == 6 {
				steps = steps[:1]
			}
			steps[0].fail = true
			steps = steps[:1]
			d := &credentialContextDB{steps: steps}
			h := NewCredentialHandler(credentialContextGorm(t, d))
			rr := httptest.NewRecorder()
			credentialContextRouter(h).ServeHTTP(rr, credentialContextRequest(t, index))
			assertCredentialContextFault(t, d, rr)
			if h.db.Statement.Context.Value(credentialContextKey{}) != nil {
				t.Fatal("shared handler context mutated")
			}
		})
	}
}
func TestCredentialReadAndRevokeStagesFollowCancellation(t *testing.T) {
	for _, index := range []int{0, 1, 4, 5, 6} {
		route := credentialContextRoutes[index]
		base := credentialAccessSteps()
		if index == 6 {
			base = base[:1]
		} else if index < 2 {
			base = append(base, credentialSQLStep{sql: `FROM "credentials"`, columns: []string{"id"}})
		} else {
			base = append(base, credentialSQLStep{sql: `UPDATE "credentials"`, affected: 1})
		}
		for stage := range base {
			t.Run(route.name+"/"+base[stage].sql, func(t *testing.T) {
				steps := append([]credentialSQLStep(nil), base[:stage+1]...)
				steps[stage].wait = true
				d := &credentialContextDB{steps: steps, started: make(chan struct{})}
				h := NewCredentialHandler(credentialContextGorm(t, d))
				rr := httptest.NewRecorder()
				r := credentialContextRequest(t, index)
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				done := make(chan struct{})
				go func() { defer close(done); credentialContextRouter(h).ServeHTTP(rr, r.WithContext(ctx)) }()
				select {
				case <-d.started:
				case <-time.After(time.Second):
					t.Fatal("SQL stage not reached")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("SQL ignored cancellation")
				}
				assertCredentialContextFault(t, d, rr)
			})
		}
	}
}
func TestCredentialAbsenceMetadataAndRefusalsStayStable(t *testing.T) {
	for _, index := range []int{0, 1, 4, 5} {
		t.Run(credentialContextRoutes[index].name, func(t *testing.T) {
			steps := credentialAccessSteps()
			status := 200
			if index < 2 {
				steps = append(steps, credentialSQLStep{sql: `FROM "credentials"`, columns: []string{"id", "name", "kind", "public_id", "secret_hash", "scope"}, values: []driver.Value{int64(21), "phone", "api_key", "public-test", "private-hash", []byte(`{"mailbox_id":7,"permissions":[]}`)}})
			} else {
				steps = append(steps, credentialSQLStep{sql: `UPDATE "credentials"`, affected: 0})
				status = 404
			}
			d := &credentialContextDB{steps: steps}
			rr := httptest.NewRecorder()
			credentialContextRouter(NewCredentialHandler(credentialContextGorm(t, d))).ServeHTTP(rr, credentialContextRequest(t, index))
			if rr.Code != status || d.lost.Load() != 0 || int(d.calls.Load()) != len(steps) || strings.Contains(rr.Body.String(), "private-hash") || strings.Contains(rr.Body.String(), "secret_hash") {
				t.Fatalf("status=%d body=%s calls=%d lost=%d", rr.Code, rr.Body, d.calls.Load(), d.lost.Load())
			}
		})
	}
	for index := range credentialContextRoutes {
		t.Run(credentialContextRoutes[index].name+"/credential refusal", func(t *testing.T) {
			d := &credentialContextDB{}
			r := credentialContextRequest(t, index)
			claims := &auth.Claims{AccountID: 7, CredentialID: 11, CredentialKind: "api_key"}
			r = r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, claims))
			rr := httptest.NewRecorder()
			credentialContextRouter(NewCredentialHandler(credentialContextGorm(t, d))).ServeHTTP(rr, r)
			want := 403
			if index == 6 {
				want = 400
			}
			if rr.Code != want || d.calls.Load() != 0 {
				t.Fatalf("status=%d body=%s SQL=%d", rr.Code, rr.Body, d.calls.Load())
			}
		})
	}
}
func TestCredentialCreationPasswordAndGrantStagesFollowRequest(t *testing.T) {
	hash, err := auth.HashPassword("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	password := []credentialSQLStep{
		{sql: `FROM "mailboxes"`, columns: []string{"id"}, values: []driver.Value{int64(7)}},
		{sql: `FROM "credentials"`, columns: []string{"id", "secret_hash"}, values: []driver.Value{int64(11), hash}},
	}
	for _, index := range []int{2, 3} {
		base := append(credentialAccessSteps(), password...)
		if index == 2 {
			base = append(base, credentialSQLStep{sql: `FROM "credentials"`, columns: []string{"id"}}, credentialSQLStep{sql: `INSERT INTO "credentials"`, columns: []string{"id"}, values: []driver.Value{int64(21)}})
		} else {
			access := credentialAccessSteps()[1:]
			base = append(base, access...)
			base = append(base, access...)
			base = append(base, credentialSQLStep{sql: `FROM "capabilities"`, columns: []string{"count"}, values: []driver.Value{int64(1)}}, credentialSQLStep{sql: `INSERT INTO "credentials"`, columns: []string{"id"}, values: []driver.Value{int64(21)}})
		}
		for stage := range base {
			for _, wait := range []bool{false, true} {
				t.Run(credentialContextRoutes[index].name+"/"+base[stage].sql+"/"+map[bool]string{false: "fault", true: "cancel"}[wait], func(t *testing.T) {
					steps := append([]credentialSQLStep(nil), base[:stage+1]...)
					steps[stage].fail = !wait
					steps[stage].wait = wait
					d := &credentialContextDB{steps: steps, started: make(chan struct{})}
					h := NewCredentialHandler(credentialContextGorm(t, d))
					rr := httptest.NewRecorder()
					r := credentialContextRequest(t, index)
					if wait {
						ctx, cancel := context.WithCancel(r.Context())
						defer cancel()
						done := make(chan struct{})
						go func() { defer close(done); credentialContextRouter(h).ServeHTTP(rr, r.WithContext(ctx)) }()
						select {
						case <-d.started:
						case <-time.After(time.Second):
							t.Fatal("creation stage not reached")
						}
						cancel()
						select {
						case <-done:
						case <-time.After(time.Second):
							t.Fatal("creation ignored cancellation")
						}
					} else {
						credentialContextRouter(h).ServeHTTP(rr, r)
					}
					assertCredentialContextFault(t, d, rr)
				})
			}
		}
	}
}

func TestCredentialRequestIsolationAndAccessControls(t *testing.T) {
	for index := 0; index < 6; index++ {
		for _, missing := range []bool{false, true} {
			t.Run(credentialContextRoutes[index].name+map[bool]string{false: "/denied", true: "/missing"}[missing], func(t *testing.T) {
				steps := credentialAccessSteps()
				if missing {
					steps = steps[:1]
					steps[0].values = nil
				} else {
					steps[0].values = []driver.Value{int64(8), "unrelated@example.test", true}
				}
				d := &credentialContextDB{steps: steps}
				rr := httptest.NewRecorder()
				credentialContextRouter(NewCredentialHandler(credentialContextGorm(t, d))).ServeHTTP(rr, credentialContextRequest(t, index))
				if rr.Code != 404 || !strings.Contains(rr.Body.String(), `"code":"not_found"`) || int(d.calls.Load()) != len(steps) || d.lost.Load() != 0 || d.begins.Load() != 0 {
					t.Fatalf("status=%d body=%s SQL=%d lost=%d begins=%d", rr.Code, rr.Body, d.calls.Load(), d.lost.Load(), d.begins.Load())
				}
			})
		}
	}
	for _, index := range []int{0, 1} {
		t.Run(credentialContextRoutes[index].name+"/cancel then reuse", func(t *testing.T) {
			steps := append(credentialAccessSteps(), credentialSQLStep{sql: `FROM "credentials"`, columns: []string{"id"}})
			d := &credentialContextDB{steps: steps}
			h := NewCredentialHandler(credentialContextGorm(t, d))
			router := credentialContextRouter(h)
			r := credentialContextRequest(t, index)
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, r.WithContext(ctx))
			if rr.Code != 500 || d.calls.Load() != 0 {
				t.Fatalf("already canceled request: %d SQL=%d", rr.Code, d.calls.Load())
			}
			rr = httptest.NewRecorder()
			router.ServeHTTP(rr, credentialContextRequest(t, index))
			if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"data":[]`) || int(d.calls.Load()) != len(steps) || d.lost.Load() != 0 || h.db.Statement.Context.Err() != nil {
				t.Fatalf("next request: %d body=%s SQL=%d lost=%d", rr.Code, rr.Body, d.calls.Load(), d.lost.Load())
			}
		})
	}
}

func TestCredentialSuccessfulWritesAndAuditUseRequestContext(t *testing.T) {
	hash, err := auth.HashPassword("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	password := []credentialSQLStep{
		{sql: `FROM "mailboxes"`, columns: []string{"id"}, values: []driver.Value{int64(7)}},
		{sql: `FROM "credentials"`, columns: []string{"id", "secret_hash"}, values: []driver.Value{int64(11), hash}},
	}
	for _, index := range []int{2, 3, 4, 5, 6} {
		base := credentialAccessSteps()
		status := 204
		if index == 6 {
			base = base[:1]
			status = 200
		} else if index == 2 || index == 3 {
			status = 201
			base = append(base, password...)
			if index == 2 {
				base = append(base, credentialSQLStep{sql: `FROM "credentials"`, columns: []string{"id"}})
			} else {
				access := credentialAccessSteps()[1:]
				base = append(base, access...)
				base = append(base, access...)
				base = append(base, credentialSQLStep{sql: `FROM "capabilities"`, columns: []string{"count"}, values: []driver.Value{int64(1)}})
			}
			base = append(base, credentialSQLStep{sql: `INSERT INTO "credentials"`, columns: []string{"id"}, values: []driver.Value{int64(21)}})
		} else {
			base = append(base, credentialSQLStep{sql: `UPDATE "credentials"`, affected: 1})
		}
		if index == 3 || index == 5 {
			base = append(base,
				credentialSQLStep{sql: "SAVEPOINT"},
				credentialSQLStep{sql: "SELECT pg_advisory_xact_lock"},
				credentialSQLStep{sql: "SELECT seq, hash FROM audit_log", columns: []string{"seq", "hash"}},
				credentialSQLStep{sql: `INSERT INTO "audit_log"`, columns: []string{"seq"}, values: []driver.Value{int64(1)}},
			)
		}
		t.Run(credentialContextRoutes[index].name, func(t *testing.T) {
			d := &credentialContextDB{steps: base}
			rr := httptest.NewRecorder()
			credentialContextRouter(NewCredentialHandler(credentialContextGorm(t, d))).ServeHTTP(rr, credentialContextRequest(t, index))
			if rr.Code != status || int(d.calls.Load()) != len(base) || d.lost.Load() != 0 || d.rollbacks.Load() != 0 || (index != 6 && d.commits.Load() != 1) {
				t.Fatalf("status=%d body=%s SQL=%d/%d lost=%d commits=%d rollbacks=%d", rr.Code, rr.Body, d.calls.Load(), len(base), d.lost.Load(), d.commits.Load(), d.rollbacks.Load())
			}
		})
		if index == 3 || index == 5 {
			for stage := len(base) - 3; stage < len(base); stage++ {
				for _, wait := range []bool{false, true} {
					t.Run(credentialContextRoutes[index].name+"/audit/"+base[stage].sql+map[bool]string{false: "/fault", true: "/cancel"}[wait], func(t *testing.T) {
						steps := append([]credentialSQLStep(nil), base[:stage+1]...)
						steps[stage].fail = !wait
						steps[stage].wait = wait
						d := &credentialContextDB{steps: steps, started: make(chan struct{})}
						rr := httptest.NewRecorder()
						r := credentialContextRequest(t, index)
						router := credentialContextRouter(NewCredentialHandler(credentialContextGorm(t, d)))
						if wait {
							ctx, cancel := context.WithCancel(r.Context())
							defer cancel()
							done := make(chan struct{})
							go func() { defer close(done); router.ServeHTTP(rr, r.WithContext(ctx)) }()
							select {
							case <-d.started:
							case <-time.After(time.Second):
								t.Fatal("audit stage not reached")
							}
							cancel()
							select {
							case <-done:
							case <-time.After(time.Second):
								t.Fatal("audit ignored cancellation")
							}
						} else {
							router.ServeHTTP(rr, r)
						}
						// Nested audit rollback may execute ROLLBACK TO SAVEPOINT after the fault;
						// it is accepted below separately from the scripted work.
						assertCredentialContextFault(t, d, rr)
					})
				}
			}
		}
	}
}
