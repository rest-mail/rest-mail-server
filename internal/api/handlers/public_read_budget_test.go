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

type publicReadBudgetKey struct{}
type publicReadStep struct {
	query   string
	columns []string
	rows    [][]driver.Value
	err     error
	wait    bool
}

// Exercise the registered handlers through real GORM/database/sql calls without
// PostgreSQL. Unscripted SQL and every mutation fail instead of becoming a
// successful empty read. The driver sees the actual transport context.
type publicReadDB struct {
	serviceProbeTestDB
	steps                             []publicReadStep
	reads, pings, writes, lost, stops int
	started                           chan struct{}
}

func (d *publicReadDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *publicReadDB) Driver() driver.Driver                        { return d }
func (d *publicReadDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *publicReadDB) Begin() (driver.Tx, error) {
	d.writes++
	return nil, errors.New("unexpected public-read transaction")
}
func (d *publicReadDB) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	d.writes++
	return nil, errors.New("unexpected public-read mutation")
}
func (d *publicReadDB) observe(ctx context.Context) {
	if _, ok := ctx.Deadline(); !ok || ctx.Value(publicReadBudgetKey{}) != true {
		d.lost++
	}
}
func (d *publicReadDB) Ping(ctx context.Context) error {
	d.pings++
	d.observe(ctx)
	return ctx.Err()
}
func (d *publicReadDB) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	d.observe(ctx)
	if rows, ok := receiveFixtureRows(query); ok && (d.reads >= len(d.steps) || !strings.Contains(query, d.steps[d.reads].query)) {
		return rows, nil
	}
	d.reads++
	if d.reads > len(d.steps) {
		return nil, errors.New("unscripted public read")
	}
	step := d.steps[d.reads-1]
	if !strings.HasPrefix(query, "SELECT ") || !strings.Contains(query, step.query) {
		return nil, errors.New("unexpected public-read SQL: " + query)
	}
	if step.wait {
		close(d.started)
		select {
		case <-ctx.Done():
			d.stops++
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
			return nil, errors.New("finite backstop: public HTTP context was lost")
		}
	}
	if step.err != nil {
		return nil, step.err
	}
	return &publicReadRows{columns: step.columns, rows: step.rows}, nil
}

type publicReadRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *publicReadRows) Columns() []string { return r.columns }
func (r *publicReadRows) Close() error      { return nil }
func (r *publicReadRows) Next(out []driver.Value) error {
	if r.index == len(r.rows) {
		return io.EOF
	}
	copy(out, r.rows[r.index])
	r.index++
	return nil
}
func publicReadGorm(t *testing.T, d *publicReadDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func publicReadRow(table string, columns []string, values ...driver.Value) publicReadStep {
	return publicReadStep{query: `FROM "` + table + `"`, columns: columns, rows: [][]driver.Value{values}}
}
func publicPolicySteps() []publicReadStep {
	return []publicReadStep{
		publicReadRow("domains", []string{"id", "name"}, int64(1), "example.test"),
		publicReadRow("mtasts_policies", []string{"id", "domain_id", "mode", "mx_hosts", "max_age", "active"}, int64(2), int64(1), "enforce", "mail.example.test", int64(86400), true),
	}
}
func publicHealthSteps(scope string) []publicReadStep {
	return []publicReadStep{
		publicReadRow("mailboxes", []string{"id", "active", "credential_epoch"}, int64(7), true, int64(0)),
		{query: "FROM grants AS g", columns: []string{"scope", "capability", "expires_at", "activation_expires_at"}, rows: [][]driver.Value{{scope, "domains:read", nil, nil}}},
		publicReadRow("mailboxes", []string{"address"}, "reader@example.test"),
		{query: "FROM grants AS g", columns: []string{"scope", "capability"}, rows: [][]driver.Value{{"*", "domains:read"}}},
		publicReadRow("certificates", []string{"id", "domain_id", "issuer", "not_after", "cert_pem", "key_pem"}, int64(5), int64(1), "fixture issuer", time.Now().Add(-time.Hour), "private-certificate-bytes", "private-key-bytes"),
		publicReadRow("domains", []string{"id", "name"}, int64(1), "example.test"),
	}
}
func publicMailboxStep(active bool) publicReadStep {
	return publicReadRow("mailboxes", []string{"id", "address", "active"}, int64(7), "reader@example.test", active)
}
func publicReadRequest(t *testing.T, path, token string) *http.Request {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), publicReadBudgetKey{}, true), 5*time.Second)
	t.Cleanup(cancel)
	r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}
func publicPolicyRoutes(db *gorm.DB) http.Handler {
	r := chi.NewRouter()
	NewMTASTSHandler(db).RegisterPolicyFile(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()})
	return r
}
func publicMailboxRoutes(db *gorm.DB) http.Handler {
	return restmailRoutes(NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{}))
}
func publicHealthRoutes(t *testing.T, db *gorm.DB, pending bool) (http.Handler, string) {
	t.Helper()
	jwt := auth.NewJWTService("public-read-fixture-key", time.Minute, time.Hour)
	pair, err := jwt.GenerateSession(7, "reader@example.test", 0, []string{auth.AMRPassword}, pending)
	if err != nil {
		t.Fatal(err)
	}
	return healthRoutes(NewHealthHandler(db, jwt)), pair.AccessToken
}
func assertPublicReadBudget(t *testing.T, d *publicReadDB, rr *httptest.ResponseRecorder, status, reads int) {
	t.Helper()
	if rr.Code != status || d.reads != reads || d.lost != 0 || d.writes != 0 || strings.Contains(rr.Body.String(), "private") {
		t.Fatalf("status=%d reads=%d/%d lost=%d writes=%d body=%s", rr.Code, d.reads, reads, d.lost, d.writes, rr.Body)
	}
}

func TestPublicPolicyAndRecipientCarryHTTPBudget(t *testing.T) {
	t.Run("MTA-STS domain and policy", func(t *testing.T) {
		d := &publicReadDB{steps: publicPolicySteps()}
		r := publicReadRequest(t, "/.well-known/mta-sts.txt", "")
		r.Host = "mta-sts.example.test"
		rr := httptest.NewRecorder()
		publicPolicyRoutes(publicReadGorm(t, d)).ServeHTTP(rr, r)
		assertPublicReadBudget(t, d, rr, 200, 2)
		if rr.Body.String() != "version: STSv1\nmode: enforce\nmx: mail.example.test\nmax_age: 86400\n" {
			t.Fatalf("policy changed: %s", rr.Body)
		}
	})
	t.Run("RESTMAIL recipient", func(t *testing.T) {
		d := &publicReadDB{steps: []publicReadStep{publicMailboxStep(true)}}
		rr := httptest.NewRecorder()
		publicMailboxRoutes(publicReadGorm(t, d)).ServeHTTP(rr, publicReadRequest(t, "/restmail/mailboxes?address=reader@example.test", ""))
		assertPublicReadBudget(t, d, rr, 200, 1)
		if !strings.Contains(rr.Body.String(), `"exists":true`) || !strings.Contains(rr.Body.String(), `"address":"reader@example.test"`) {
			t.Fatalf("recipient changed: %s", rr.Body)
		}
	})
}

func TestHealthPrivilegedReadsCarryHTTPBudget(t *testing.T) {
	d := &publicReadDB{steps: publicHealthSteps(grants.All)}
	handler, token := publicHealthRoutes(t, publicReadGorm(t, d), false)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, publicReadRequest(t, "/api/health", token))
	assertPublicReadBudget(t, d, rr, 200, 6)
	if d.pings != 1 || !strings.Contains(rr.Body.String(), `"cert_warnings"`) || !strings.Contains(rr.Body.String(), `"domain":"example.test"`) {
		t.Fatalf("privileged health changed: ping=%d body=%s", d.pings, rr.Body)
	}
}

func TestPublicReadCancellationReachesEveryStage(t *testing.T) {
	for _, area := range []string{"policy", "recipient", "health"} {
		count := map[string]int{"policy": 2, "recipient": 1, "health": 6}[area]
		for index := 0; index < count; index++ {
			t.Run(area+"/"+string(rune('1'+index)), func(t *testing.T) {
				var steps []publicReadStep
				path := "/api/health"
				switch area {
				case "policy":
					steps, path = publicPolicySteps(), "/.well-known/mta-sts.txt"
				case "recipient":
					steps, path = []publicReadStep{publicMailboxStep(true)}, "/restmail/mailboxes?address=reader@example.test"
				case "health":
					steps = publicHealthSteps(grants.All)
				}
				steps = steps[:index+1]
				steps[index].wait = true
				d := &publicReadDB{steps: steps, started: make(chan struct{})}
				db := publicReadGorm(t, d)
				var handler http.Handler
				token := ""
				status := 500
				switch area {
				case "policy":
					handler = publicPolicyRoutes(db)
				case "recipient":
					handler = publicMailboxRoutes(db)
				case "health":
					handler, token = publicHealthRoutes(t, db, false)
					status = 200 // Existing auxiliary-warning failures are non-fatal.
				}
				r := publicReadRequest(t, path, token)
				r.Host = "mta-sts.example.test"
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				r = r.WithContext(ctx)
				rr := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { defer close(done); handler.ServeHTTP(rr, r) }()
				select {
				case <-d.started:
					cancel()
				case <-time.After(time.Second):
					cancel()
					t.Error("scripted read never started")
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("handler did not return within finite driver backstop")
				}
				assertPublicReadBudget(t, d, rr, status, index+1)
				if d.stops != 1 {
					t.Fatalf("cancelled driver reads=%d, want1", d.stops)
				}
			})
		}
	}
}

func TestPublicReadFaultsPreserveAbsenceAndPrivacy(t *testing.T) {
	for index := 0; index < 2; index++ {
		for _, missing := range []bool{false, true} {
			t.Run(string(rune('1'+index))+map[bool]string{false: "/fault", true: "/absent"}[missing], func(t *testing.T) {
				steps := publicPolicySteps()[:index+1]
				steps[index].err = errors.New("private database topology detail")
				status := 500
				if missing {
					steps[index].err, steps[index].rows = nil, nil
					status = 404
				}
				d := &publicReadDB{steps: steps}
				r := publicReadRequest(t, "/.well-known/mta-sts.txt", "")
				r.Host = "mta-sts.example.test"
				rr := httptest.NewRecorder()
				publicPolicyRoutes(publicReadGorm(t, d)).ServeHTTP(rr, r)
				assertPublicReadBudget(t, d, rr, status, index+1)
			})
		}
	}
	for _, kind := range []string{"fault", "absent", "inactive"} {
		t.Run("recipient/"+kind, func(t *testing.T) {
			step := publicMailboxStep(kind != "inactive")
			status := 200
			if kind == "fault" {
				step.err, status = errors.New("private database topology detail"), 500
			} else if kind == "absent" {
				step.rows = nil
			}
			d := &publicReadDB{steps: []publicReadStep{step}}
			rr := httptest.NewRecorder()
			publicMailboxRoutes(publicReadGorm(t, d)).ServeHTTP(rr, publicReadRequest(t, "/restmail/mailboxes?address=reader@example.test", ""))
			assertPublicReadBudget(t, d, rr, status, 1)
			if kind != "fault" && !strings.Contains(rr.Body.String(), `"exists":false`) {
				t.Fatalf("negative identity changed: %s", rr.Body)
			}
		})
	}
}

func TestHealthReadBudgetRetainsDisclosureGate(t *testing.T) {
	for _, kind := range []string{"anonymous", "invalid token", "setup pending", "stale session", "no grant", "domain scoped"} {
		t.Run(kind, func(t *testing.T) {
			steps := publicHealthSteps(grants.All)
			want := 0
			switch kind {
			case "stale session":
				steps[0].rows[0][2] = int64(1)
				want = 1
			case "no grant":
				steps[1].rows = nil
				want = 4
			case "domain scoped":
				steps[1].rows[0][0] = grants.Domain("example.test")
				want = 4
			}
			d := &publicReadDB{steps: steps[:want]}
			handler, token := publicHealthRoutes(t, publicReadGorm(t, d), kind == "setup pending")
			if kind == "anonymous" {
				token = ""
			} else if kind == "invalid token" {
				token = "invalid-fixture-token"
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, publicReadRequest(t, "/api/health", token))
			assertPublicReadBudget(t, d, rr, 200, want)
			if d.pings != 1 || strings.Contains(rr.Body.String(), "cert_warnings") {
				t.Fatalf("certificate inventory escaped its gate: %s", rr.Body)
			}
		})
	}
}
