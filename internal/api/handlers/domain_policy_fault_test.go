package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type domainPolicySQLStep struct {
	match      string
	columns    []string
	values     []driver.Value
	affected   int64
	fail, wait bool
}
type domainPolicyFaultDB struct {
	serviceProbeTestDB
	steps                                            []domainPolicySQLStep
	calls, lost, begins, commits, rollbacks, inserts atomic.Int32
	started                                          chan struct{}
	lastWrite                                        string
	lastArgs                                         []driver.NamedValue
}
type domainPolicyContextKey struct{}

func (d *domainPolicyFaultDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *domainPolicyFaultDB) Driver() driver.Driver                        { return d }
func (d *domainPolicyFaultDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *domainPolicyFaultDB) observe(ctx context.Context) {
	if _, ok := ctx.Deadline(); !ok || ctx.Value(domainPolicyContextKey{}) != true {
		d.lost.Add(1)
	}
}
func (d *domainPolicyFaultDB) step(ctx context.Context, q string, args []driver.NamedValue) (domainPolicySQLStep, error) {
	d.observe(ctx)
	i := int(d.calls.Add(1)) - 1
	if strings.HasPrefix(q, "INSERT ") {
		d.inserts.Add(1)
	}
	if !strings.HasPrefix(q, "SELECT ") {
		d.lastWrite = q
		d.lastArgs = append([]driver.NamedValue(nil), args...)
	}
	if i >= len(d.steps) {
		return domainPolicySQLStep{}, errors.New("unexpected policy SQL")
	}
	s := d.steps[i]
	if !strings.Contains(q, s.match) {
		return s, errors.New("unexpected policy statement: " + q)
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
		return s, errors.New("private domain policy dependency failure")
	}
	return s, nil
}
func (d *domainPolicyFaultDB) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	s, err := d.step(ctx, q, args)
	if err != nil {
		return nil, err
	}
	return &deliveryReadRows{columns: s.columns, values: s.values}, nil
}
func (d *domainPolicyFaultDB) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	s, err := d.step(ctx, q, args)
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(s.affected), nil
}
func (d *domainPolicyFaultDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.observe(ctx)
	d.begins.Add(1)
	return &domainPolicyFaultTx{d}, nil
}

type domainPolicyFaultTx struct{ d *domainPolicyFaultDB }

func (tx *domainPolicyFaultTx) Commit() error   { tx.d.commits.Add(1); return nil }
func (tx *domainPolicyFaultTx) Rollback() error { tx.d.rollbacks.Add(1); return nil }
func domainPolicyFaultGorm(t *testing.T, d *domainPolicyFaultDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func domainPolicyDomainStep() domainPolicySQLStep {
	return domainPolicySQLStep{match: `FROM "domains"`, columns: []string{"id", "name", "active"}, values: []driver.Value{int64(7), "example.test", true}}
}
func domainPolicyRuleStep(kind string) domainPolicySQLStep {
	return domainPolicySQLStep{match: `FROM "domain_sender_rules"`, columns: []string{"id", "domain_id", "pattern", "list_type"}, values: []driver.Value{int64(8), int64(7), "*@example.test", kind}}
}
func domainPolicyMTAStep() domainPolicySQLStep {
	return domainPolicySQLStep{match: `FROM "mtasts_policies"`, columns: []string{"id", "domain_id", "mode", "mx_hosts", "max_age", "active"}, values: []driver.Value{int64(8), int64(7), "enforce", "old.example.test", int64(86400), true}}
}
func domainPolicyRelayStep() domainPolicySQLStep {
	return domainPolicySQLStep{match: `FROM "domain_relays"`, columns: []string{"id", "domain_id", "transport", "host", "port", "tls_mode", "username", "password_enc"}, values: []driver.Value{int64(8), int64(7), "relay", "smtp.example.test", int64(465), "implicit", "operator", "private-ciphertext"}}
}
func domainPolicyInsert(table string) domainPolicySQLStep {
	return domainPolicySQLStep{match: `INSERT INTO "` + table + `"`, columns: []string{"id"}, values: []driver.Value{int64(9)}}
}

var domainPolicyFaultRoutes = []struct {
	name, method, path, body string
	status                   int
}{
	{"list allow", "GET", "/api/v1/admin/domains/7/allowlist", "", 200},
	{"add allow", "POST", "/api/v1/admin/domains/7/allowlist", `{"pattern":"*@example.test"}`, 201},
	{"delete allow", "DELETE", "/api/v1/admin/domains/7/allowlist/8", "", 204},
	{"list block", "GET", "/api/v1/admin/domains/7/blocklist", "", 200},
	{"add block", "POST", "/api/v1/admin/domains/7/blocklist", `{"pattern":"*@example.test"}`, 201},
	{"delete block", "DELETE", "/api/v1/admin/domains/7/blocklist/8", "", 204},
	{"list suppressions", "GET", "/api/v1/admin/domains/7/suppressions", "", 200},
	{"clear suppression", "DELETE", "/api/v1/admin/domains/7/suppressions/8", "", 204},
	{"get MTA-STS", "GET", "/api/v1/admin/domains/7/mta-sts", "", 200},
	{"set MTA-STS", "PUT", "/api/v1/admin/domains/7/mta-sts", `{"mode":"none","mx_hosts":"mx.example.test","max_age":0,"active":false}`, 200},
	{"delete MTA-STS", "DELETE", "/api/v1/admin/domains/7/mta-sts", "", 204},
	{"get relay", "GET", "/api/v1/admin/domains/7/relay", "", 200},
	{"set relay", "PUT", "/api/v1/admin/domains/7/relay", `{"transport":"direct"}`, 200},
}

func domainPolicySteps(index int) []domainPolicySQLStep {
	switch index {
	case 0, 3:
		return []domainPolicySQLStep{domainPolicyRuleStep(map[int]string{0: "allow", 3: "block"}[index])}
	case 1, 4:
		return []domainPolicySQLStep{domainPolicyDomainStep(), domainPolicyInsert("domain_sender_rules")}
	case 2, 5:
		return []domainPolicySQLStep{domainPolicyRuleStep(map[int]string{2: "allow", 5: "block"}[index]), {match: `DELETE FROM "domain_sender_rules"`, affected: 1}}
	case 6:
		return []domainPolicySQLStep{{match: `FROM "suppressions"`, columns: []string{"id", "domain_id", "address"}, values: []driver.Value{int64(8), int64(7), "recipient@example.test"}}}
	case 7:
		return []domainPolicySQLStep{{match: `DELETE FROM "suppressions"`, affected: 1}}
	case 8:
		return []domainPolicySQLStep{domainPolicyDomainStep(), domainPolicyMTAStep()}
	case 9:
		return []domainPolicySQLStep{domainPolicyDomainStep(), domainPolicyMTAStep(), {match: `UPDATE "mtasts_policies"`, affected: 1}}
	case 10:
		return []domainPolicySQLStep{{match: `DELETE FROM "mtasts_policies"`, affected: 1}}
	case 11:
		return []domainPolicySQLStep{domainPolicyDomainStep(), domainPolicyRelayStep()}
	case 12:
		return []domainPolicySQLStep{domainPolicyDomainStep(), domainPolicyRelayStep(), {match: `UPDATE "domain_relays"`, affected: 1}}
	}
	panic("unknown route")
}
func domainPolicyFaultRouter(db *gorm.DB, checks *DomainPolicyRouteChecks) http.Handler {
	r := chi.NewRouter()
	rs := endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}
	pass := func(string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
	}
	c := DomainPolicyRouteChecks{At: pass, OnDomain: pass}
	if checks != nil {
		c = *checks
	}
	NewSenderRuleHandler(db).Register(rs, c)
	NewSuppressionHandler(db).Register(rs, c)
	NewMTASTSHandler(db).Register(rs, c)
	NewRelayHandler(db, relayTestMasterKey).Register(rs, c)
	return r
}
func domainPolicyFaultRequest(t *testing.T, index int) *http.Request {
	t.Helper()
	route := domainPolicyFaultRoutes[index]
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), domainPolicyContextKey{}, true), 4*time.Second)
	t.Cleanup(cancel)
	r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	return r
}
func assertDomainPolicyFault(t *testing.T, d *domainPolicyFaultDB, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != 500 || !strings.Contains(rr.Body.String(), `"code":"internal_error"`) || strings.Contains(rr.Body.String(), "private") || strings.Contains(rr.Body.String(), "password_enc") || d.lost.Load() != 0 || d.commits.Load() != 0 || int(d.calls.Load()) != len(d.steps) {
		t.Fatalf("status=%d body=%s calls=%d/%d lost=%d commits=%d rollbacks=%d", rr.Code, rr.Body, d.calls.Load(), len(d.steps), d.lost.Load(), d.commits.Load(), d.rollbacks.Load())
	}
}
func TestDomainPolicyOperationsUseHTTPContext(t *testing.T) {
	for index, route := range domainPolicyFaultRoutes {
		t.Run(route.name, func(t *testing.T) {
			d := &domainPolicyFaultDB{steps: domainPolicySteps(index)}
			rr := httptest.NewRecorder()
			domainPolicyFaultRouter(domainPolicyFaultGorm(t, d), nil).ServeHTTP(rr, domainPolicyFaultRequest(t, index))
			if rr.Code != route.status || d.lost.Load() != 0 || int(d.calls.Load()) != len(d.steps) || strings.Contains(rr.Body.String(), "private") || strings.Contains(rr.Body.String(), "password_enc") {
				t.Fatalf("status=%d body=%s calls=%d/%d lost=%d", rr.Code, rr.Body, d.calls.Load(), len(d.steps), d.lost.Load())
			}
		})
	}
}
func TestDomainPolicyStaleWritesDoNotRecreateOrSucceed(t *testing.T) {
	for _, index := range []int{2, 5, 9, 12} {
		t.Run(domainPolicyFaultRoutes[index].name, func(t *testing.T) {
			steps := domainPolicySteps(index)
			steps[len(steps)-1].affected = 0
			expectedCalls := len(steps)
			status := 404
			if index == 9 || index == 12 {
				status = 500
				table := map[int]string{9: "mtasts_policies", 12: "domain_relays"}[index]
				steps = append(steps, domainPolicyInsert(table))
			}
			d := &domainPolicyFaultDB{steps: steps}
			rr := httptest.NewRecorder()
			domainPolicyFaultRouter(domainPolicyFaultGorm(t, d), nil).ServeHTTP(rr, domainPolicyFaultRequest(t, index))
			if rr.Code != status || int(d.calls.Load()) != expectedCalls || d.inserts.Load() != 0 {
				t.Fatalf("status=%d body=%s SQL=%d want=%d fallback inserts=%d", rr.Code, rr.Body, d.calls.Load(), expectedCalls, d.inserts.Load())
			}
			if !strings.Contains(d.lastWrite, "domain_id") || (index == 2 || index == 5) && !strings.Contains(d.lastWrite, "list_type") {
				t.Fatalf("mutation lost domain/list guard: %s", d.lastWrite)
			}
		})
	}
}
func TestDomainPolicySQLFaultsStayPrivate(t *testing.T) {
	for index, route := range domainPolicyFaultRoutes {
		base := domainPolicySteps(index)
		for stage := range base {
			t.Run(route.name+"/"+base[stage].match, func(t *testing.T) {
				steps := append([]domainPolicySQLStep(nil), base[:stage+1]...)
				steps[stage].fail = true
				d := &domainPolicyFaultDB{steps: steps}
				rr := httptest.NewRecorder()
				domainPolicyFaultRouter(domainPolicyFaultGorm(t, d), nil).ServeHTTP(rr, domainPolicyFaultRequest(t, index))
				assertDomainPolicyFault(t, d, rr)
			})
		}
	}
}
func TestDomainPolicyCancellationReachesEverySQLStage(t *testing.T) {
	for index, route := range domainPolicyFaultRoutes {
		base := domainPolicySteps(index)
		for stage := range base {
			t.Run(route.name+"/"+base[stage].match, func(t *testing.T) {
				steps := append([]domainPolicySQLStep(nil), base[:stage+1]...)
				steps[stage].wait = true
				d := &domainPolicyFaultDB{steps: steps, started: make(chan struct{})}
				router := domainPolicyFaultRouter(domainPolicyFaultGorm(t, d), nil)
				r := domainPolicyFaultRequest(t, index)
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				rr := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { defer close(done); router.ServeHTTP(rr, r.WithContext(ctx)) }()
				select {
				case <-d.started:
				case <-time.After(time.Second):
					t.Fatal("policy stage not reached")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("SQL ignored cancellation")
				}
				assertDomainPolicyFault(t, d, rr)
			})
		}
	}
}
func TestDomainPolicyAbsenceAndExplicitZerosStayStable(t *testing.T) {
	for _, index := range []int{0, 3, 6, 8, 9, 11, 12, 2, 5, 7, 10} {
		t.Run(domainPolicyFaultRoutes[index].name, func(t *testing.T) {
			steps := domainPolicySteps(index)
			status := 200
			switch index {
			case 0, 3, 6:
				steps[0].values = nil
			case 8:
				steps[1].values = nil
				status = 404
			case 9:
				steps[1].values = nil
				steps[2] = domainPolicyInsert("mtasts_policies")
				status = 201
			case 11:
				steps[1].values = nil
			case 12:
				steps[1].values = nil
				steps[2] = domainPolicyInsert("domain_relays")
			case 2, 5:
				steps = steps[:1]
				steps[0].values = nil
				status = 404
			case 7, 10:
				steps[0].affected = 0
				status = 404
			}
			d := &domainPolicyFaultDB{steps: steps}
			rr := httptest.NewRecorder()
			domainPolicyFaultRouter(domainPolicyFaultGorm(t, d), nil).ServeHTTP(rr, domainPolicyFaultRequest(t, index))
			if rr.Code != status || d.lost.Load() != 0 || int(d.calls.Load()) != len(steps) {
				t.Fatalf("status=%d body=%s SQL=%d/%d lost=%d", rr.Code, rr.Body, d.calls.Load(), len(steps), d.lost.Load())
			}
			if index == 9 && (!strings.Contains(rr.Body.String(), `"max_age":0`) || !strings.Contains(rr.Body.String(), `"active":false`)) {
				t.Fatalf("explicit MTA-STS zeros lost: %s", rr.Body)
			}
			if index == 12 && (strings.Contains(rr.Body.String(), "private") || !strings.Contains(rr.Body.String(), `"password_set":false`)) {
				t.Fatalf("direct transport did not clear relay: %s", rr.Body)
			}
		})
	}
}
func TestDomainPolicyCapabilityChecksStillPrecedeBinding(t *testing.T) {
	for index, route := range domainPolicyFaultRoutes {
		t.Run(route.name, func(t *testing.T) {
			d := &domainPolicyFaultDB{}
			scope, capability := "", ""
			check := func(where string) func(string) endpoint.Middleware {
				return func(cap string) endpoint.Middleware {
					return endpoint.Middleware{Wrap: func(http.Handler) http.Handler {
						return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { scope, capability = where, cap; w.WriteHeader(403) })
					}}
				}
			}
			checks := DomainPolicyRouteChecks{OnDomain: check("domain"), At: check("all")}
			r := domainPolicyFaultRequest(t, index)
			r.Body = http.NoBody
			r.ContentLength = 5
			rr := httptest.NewRecorder()
			domainPolicyFaultRouter(domainPolicyFaultGorm(t, d), &checks).ServeHTTP(rr, r)
			wantScope := "domain"
			if index == 12 {
				wantScope = "all"
			}
			wantCap := "domains:write"
			if route.method == "GET" {
				wantCap = "domains:read"
			}
			if rr.Code != 403 || d.calls.Load() != 0 || scope != wantScope || capability != wantCap {
				t.Fatalf("status=%d SQL=%d boundary=%s/%s", rr.Code, d.calls.Load(), scope, capability)
			}
		})
	}
}

func domainPolicyBoundWrites(d *domainPolicyFaultDB) map[string]any {
	out := map[string]any{}
	for _, m := range regexp.MustCompile(`"([a-z_]+)"=\$(\d+)`).FindAllStringSubmatch(d.lastWrite, -1) {
		n, _ := strconv.Atoi(m[2])
		out[m[1]] = d.lastArgs[n-1].Value
	}
	return out
}
func TestDomainPolicyBoundUpdatesPreserveExplicitZeroValues(t *testing.T) {
	for _, index := range []int{9, 12} {
		t.Run(domainPolicyFaultRoutes[index].name, func(t *testing.T) {
			d := &domainPolicyFaultDB{steps: domainPolicySteps(index)}
			rr := httptest.NewRecorder()
			domainPolicyFaultRouter(domainPolicyFaultGorm(t, d), nil).ServeHTTP(rr, domainPolicyFaultRequest(t, index))
			if rr.Code != 200 || d.inserts.Load() != 0 {
				t.Fatalf("status=%d inserts=%d", rr.Code, d.inserts.Load())
			}
			got := domainPolicyBoundWrites(d)
			want := map[string]any{"mode": "none", "mx_hosts": "mx.example.test", "max_age": int64(0), "active": false}
			if index == 12 {
				want = map[string]any{"transport": "direct", "host": "", "port": int64(0), "tls_mode": "", "username": "", "password_enc": ""}
			}
			for key, value := range want {
				if got[key] != value {
					t.Errorf("explicit field %s missing or changed in SQL", key)
				}
			}
			foundDomain, foundID := false, false
			for _, arg := range d.lastArgs {
				foundDomain = foundDomain || arg.Value == int64(7)
				foundID = foundID || arg.Value == int64(8)
			}
			if !foundDomain || !foundID {
				t.Fatal("selected policy/domain guards not bound")
			}
		})
	}
}
func TestDomainPolicyRelayPasswordRemainsWriteOnly(t *testing.T) {
	for _, mode := range []string{"retain", "clear", "replace", "master key absent"} {
		t.Run(mode, func(t *testing.T) {
			steps := domainPolicySteps(12)
			body := `{"transport":"relay","host":"smtp2.example.test","port":587,"tls_mode":"starttls","username":"operator"}`
			if mode == "clear" {
				body = `{"transport":"relay","host":"smtp2.example.test","port":587,"tls_mode":"starttls","username":"","password":""}`
			}
			if mode == "replace" || mode == "master key absent" {
				body = `{"transport":"relay","host":"smtp2.example.test","port":587,"tls_mode":"starttls","username":"operator","password":"fixture-relay-secret"}`
			}
			if mode == "master key absent" {
				steps = steps[:2]
			}
			d := &domainPolicyFaultDB{steps: steps}
			db := domainPolicyFaultGorm(t, d)
			router := domainPolicyFaultRouter(db, nil)
			if mode == "master key absent" {
				router = policyRoutes(NewRelayHandler(db, ""))
			}
			r := domainPolicyFaultRequest(t, 12)
			r.Body = io.NopCloser(strings.NewReader(body))
			r.ContentLength = int64(len(body))
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, r)
			status := 200
			if mode == "master key absent" {
				status = 409
			}
			if rr.Code != status || int(d.calls.Load()) != len(steps) || d.lost.Load() != 0 || strings.Contains(rr.Body.String(), "fixture-relay-secret") || strings.Contains(rr.Body.String(), "private-ciphertext") || strings.Contains(rr.Body.String(), "password_enc") {
				t.Fatalf("status=%d body=%s calls=%d/%d lost=%d", rr.Code, rr.Body, d.calls.Load(), len(steps), d.lost.Load())
			}
			if mode == "master key absent" {
				return
			}
			cipher, _ := domainPolicyBoundWrites(d)["password_enc"].(string)
			switch mode {
			case "retain":
				if cipher != "private-ciphertext" {
					t.Fatal("omitted password changed ciphertext")
				}
			case "clear":
				if cipher != "" || !strings.Contains(rr.Body.String(), `"password_set":false`) {
					t.Fatal("empty password did not clear")
				}
			case "replace":
				if !strings.HasPrefix(cipher, models.RelayPasswordCipherPrefix) || strings.Contains(cipher, "fixture-relay-secret") {
					t.Fatal("new password was not encrypted")
				}
				plaintext, err := models.LoadRelayPassword(cipher, relayTestMasterKey)
				if err != nil || plaintext != "fixture-relay-secret" {
					t.Fatal("stored replacement did not decrypt")
				}
			}
		})
	}
}

func TestDomainPolicyMissingDomainsKeepExistingRefusals(t *testing.T) {
	for _, index := range []int{1, 4, 8, 9, 11, 12} {
		t.Run(domainPolicyFaultRoutes[index].name, func(t *testing.T) {
			steps := domainPolicySteps(index)[:1]
			steps[0].values = nil
			d := &domainPolicyFaultDB{steps: steps}
			rr := httptest.NewRecorder()
			domainPolicyFaultRouter(domainPolicyFaultGorm(t, d), nil).ServeHTTP(rr, domainPolicyFaultRequest(t, index))
			code := "not_found"
			if index == 1 || index == 4 {
				code = "domain_not_found"
			}
			if rr.Code != 404 || !strings.Contains(rr.Body.String(), `"code":"`+code+`"`) || d.calls.Load() != 1 || d.lost.Load() != 0 || d.inserts.Load() != 0 || d.begins.Load() != 0 {
				t.Fatalf("status=%d body=%s SQL=%d inserts=%d transactions=%d lost=%d", rr.Code, rr.Body, d.calls.Load(), d.inserts.Load(), d.begins.Load(), d.lost.Load())
			}
		})
	}
}
func TestDomainPolicyHandlersRemainUsableAfterCancellation(t *testing.T) {
	for index, route := range domainPolicyFaultRoutes {
		t.Run(route.name, func(t *testing.T) {
			d := &domainPolicyFaultDB{steps: domainPolicySteps(index)}
			router := domainPolicyFaultRouter(domainPolicyFaultGorm(t, d), nil)
			r := domainPolicyFaultRequest(t, index)
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, r.WithContext(ctx))
			if rr.Code != 500 || d.calls.Load() != 0 {
				t.Fatalf("canceled request: status=%d driver SQL=%d", rr.Code, d.calls.Load())
			}
			rr = httptest.NewRecorder()
			router.ServeHTTP(rr, domainPolicyFaultRequest(t, index))
			if rr.Code != route.status || int(d.calls.Load()) != len(d.steps) || d.lost.Load() != 0 {
				t.Fatalf("handler reuse: status=%d SQL=%d/%d lost=%d", rr.Code, d.calls.Load(), len(d.steps), d.lost.Load())
			}
		})
	}
}
