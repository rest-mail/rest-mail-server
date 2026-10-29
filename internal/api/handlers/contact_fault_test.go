package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
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

type contactFaultContextKey struct{}
type contactFaultDB struct {
	serviceProbeTestDB
	mu                                          sync.Mutex
	read, write, begin, commit, rollback, lost  int
	failRead                                    int
	failWrite, zeroWrite, missing, denied, wait bool
	started                                     chan struct{}
	name, trust, pendingName, pendingTrust      string
	pending                                     bool
	rolledBack                                  chan struct{}
	writeSQL                                    string
}

func (d *contactFaultDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *contactFaultDB) Driver() driver.Driver                        { return d }
func (d *contactFaultDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *contactFaultDB) observe(ctx context.Context) {
	if _, ok := ctx.Deadline(); !ok || ctx.Value(contactFaultContextKey{}) != true {
		d.lost++
	}
}
func (d *contactFaultDB) fault(ctx context.Context) error {
	if d.wait {
		close(d.started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return errors.New("private contact HTTP cancellation lost")
		}
	}
	return errors.New("private contact dependency failure")
}
func (d *contactFaultDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.read++
	d.observe(ctx)
	if d.failRead == d.read {
		return nil, d.fault(ctx)
	}
	if rows, ok := defaultGrantRows(q); ok {
		return rows, nil
	}
	if strings.Contains(q, `FROM "mailboxes"`) {
		if strings.Contains(q, `SELECT "address"`) {
			return &deliveryReadRows{columns: []string{"address"}, values: []driver.Value{"reader@example.test"}}, nil
		}
		id := int64(7)
		if d.denied {
			id = 8
		}
		return &deliveryReadRows{columns: []string{"id", "address", "active"}, values: []driver.Value{id, "reader@example.test", true}}, nil
	}
	if strings.Contains(q, `count(*)`) {
		return &deliveryReadRows{columns: []string{"count"}, values: []driver.Value{int64(1)}}, nil
	}
	if strings.Contains(q, `FROM "contacts"`) {
		columns := []string{"id", "mailbox_id", "email", "name", "trust_level", "source"}
		if d.missing {
			return &deliveryReadRows{columns: columns}, nil
		}
		name, trust := d.name, d.trust
		if d.pending {
			name, trust = d.pendingName, d.pendingTrust
		}
		return &deliveryReadRows{columns: columns, values: []driver.Value{int64(21), int64(7), "friend@example.test", name, trust, "manual"}}, nil
	}
	if strings.Contains(q, `INSERT INTO "contacts"`) {
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(21)}}, nil
	}
	return nil, errors.New("unexpected contact SQL: " + q)
}

var contactAssignments = regexp.MustCompile(`"(name|trust_level)"=\$(\d+)`)

func (d *contactFaultDB) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.write++
	d.observe(ctx)
	d.writeSQL = q
	if d.failWrite {
		return nil, d.fault(ctx)
	}
	if d.zeroWrite {
		return driver.RowsAffected(0), nil
	}
	d.pending = true
	d.pendingName, d.pendingTrust = d.name, d.trust
	for _, m := range contactAssignments.FindAllStringSubmatch(q, -1) {
		n, _ := strconv.Atoi(m[2])
		s, _ := args[n-1].Value.(string)
		if m[1] == "name" {
			d.pendingName = s
		} else {
			d.pendingTrust = s
		}
	}
	return driver.RowsAffected(1), nil
}
func (d *contactFaultDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observe(ctx)
	d.begin++
	return &contactFaultTx{d}, nil
}

type contactFaultTx struct{ d *contactFaultDB }

func (tx *contactFaultTx) Commit() error {
	d := tx.d
	d.mu.Lock()
	defer d.mu.Unlock()
	d.commit++
	if d.pending {
		d.name, d.trust = d.pendingName, d.pendingTrust
	}
	d.pending = false
	return nil
}
func (tx *contactFaultTx) Rollback() error {
	d := tx.d
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rollback++
	d.pending = false
	close(d.rolledBack)
	return nil
}
func contactFaultGorm(t *testing.T, d *contactFaultDB) *gorm.DB {
	t.Helper()
	d.rolledBack = make(chan struct{})
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

var contactFaultRoutes = []struct{ name, method, path, body string }{
	{"list", "GET", "/api/v1/accounts/7/contacts", ""},
	{"create", "POST", "/api/v1/accounts/7/contacts", `{"email":"friend@example.test","name":"Friend"}`},
	{"update", "PATCH", "/api/v1/accounts/7/contacts/21", `{"name":"Changed"}`},
	{"delete", "DELETE", "/api/v1/accounts/7/contacts/21", ""},
	{"block", "POST", "/api/v1/accounts/7/contacts/block", `{"email":"friend@example.test"}`},
	{"import", "POST", "/api/v1/accounts/7/contacts/import", `{"contacts":[{"email":"friend@example.test"}]}`},
	{"suggest", "GET", "/api/v1/accounts/7/contacts/suggest?q=friend", ""},
}

func contactFaultRouter(db *gorm.DB) http.Handler {
	r := chi.NewRouter()
	NewContactHandler(db).RegisterContacts(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()})
	return r
}
func contactFaultRequest(t *testing.T, index int) *http.Request {
	t.Helper()
	route := contactFaultRoutes[index]
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), contactFaultContextKey{}, true), 4*time.Second)
	t.Cleanup(cancel)
	ctx = context.WithValue(ctx, middleware.ClaimsKey, &auth.Claims{AccountID: 7, Address: "reader@example.test", CredentialID: 11, CredentialKind: "app_password"})
	r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	return r
}
func assertContactFault(t *testing.T, d *contactFaultDB, rr *httptest.ResponseRecorder, status int) {
	t.Helper()
	// database/sql can take ownership of rollback when the context is canceled.
	// Wait for the driver's completion signal before examining pending state.
	d.mu.Lock()
	needsRollback := status >= 400 && d.begin > 0 && d.commit == 0
	d.mu.Unlock()
	if needsRollback {
		select {
		case <-d.rolledBack:
		case <-time.After(time.Second):
			t.Fatal("transaction rollback did not finish")
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if rr.Code != status || strings.Contains(rr.Body.String(), "private") || d.lost != 0 || (status >= 400 && (d.commit != 0 || d.pending)) {
		t.Fatalf("status=%d body=%s reads=%d writes=%d lost=%d commits=%d rollbacks=%d pending=%v", rr.Code, rr.Body, d.read, d.write, d.lost, d.commit, d.rollback, d.pending)
	}
}
func TestContactReloadFailureRollsBackMutation(t *testing.T) {
	for _, index := range []int{2, 4} {
		t.Run(contactFaultRoutes[index].name, func(t *testing.T) {
			d := &contactFaultDB{name: "Original", trust: "trusted", failRead: accessReads + 2}
			rr := httptest.NewRecorder()
			contactFaultRouter(contactFaultGorm(t, d)).ServeHTTP(rr, contactFaultRequest(t, index))
			d.mu.Lock()
			defer d.mu.Unlock()
			if rr.Code != 500 || d.name != "Original" || d.trust != "trusted" || d.commit != 0 || d.rollback != 1 || d.pending {
				t.Fatalf("status=%d body=%s persisted=(%q,%q) commits=%d rollbacks=%d pending=%v", rr.Code, rr.Body, d.name, d.trust, d.commit, d.rollback, d.pending)
			}
		})
	}
}
func TestContactRoutesCarryCancellationThroughAccess(t *testing.T) {
	for index, route := range contactFaultRoutes {
		t.Run(route.name, func(t *testing.T) {
			d := &contactFaultDB{failRead: 1, wait: true, started: make(chan struct{})}
			r := contactFaultRequest(t, index)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			rr := httptest.NewRecorder()
			router := contactFaultRouter(contactFaultGorm(t, d))
			done := make(chan struct{})
			go func() { defer close(done); router.ServeHTTP(rr, r.WithContext(ctx)) }()
			select {
			case <-d.started:
			case <-time.After(time.Second):
				t.Fatal("access not reached")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("access ignored HTTP cancellation")
			}
			assertContactFault(t, d, rr, 500)
		})
	}
}
func TestContactReadAndMutationFaults(t *testing.T) {
	for _, index := range []int{0, 2, 3, 4, 6} {
		stages := []int{accessReads + 1}
		if index == 0 || index == 2 || index == 4 {
			stages = append(stages, accessReads+2)
		}
		for _, read := range stages {
			for _, wait := range []bool{false, true} {
				t.Run(contactFaultRoutes[index].name+"/read"+strconv.Itoa(read)+map[bool]string{false: "/fault", true: "/cancel"}[wait], func(t *testing.T) {
					d := &contactFaultDB{name: "Original", trust: "trusted", failRead: read, wait: wait, started: make(chan struct{})}
					rr := httptest.NewRecorder()
					router := contactFaultRouter(contactFaultGorm(t, d))
					r := contactFaultRequest(t, index)
					if wait {
						ctx, cancel := context.WithCancel(r.Context())
						defer cancel()
						done := make(chan struct{})
						go func() { defer close(done); router.ServeHTTP(rr, r.WithContext(ctx)) }()
						select {
						case <-d.started:
						case <-time.After(time.Second):
							t.Fatal("contact read not reached")
						}
						cancel()
						select {
						case <-done:
						case <-time.After(time.Second):
							t.Fatal("contact read ignored cancellation")
						}
					} else {
						router.ServeHTTP(rr, r)
					}
					assertContactFault(t, d, rr, 500)
				})
			}
		}
	}
	for _, index := range []int{2, 3, 4} {
		t.Run(contactFaultRoutes[index].name+"/write failure", func(t *testing.T) {
			d := &contactFaultDB{name: "Original", trust: "trusted", failWrite: true}
			rr := httptest.NewRecorder()
			contactFaultRouter(contactFaultGorm(t, d)).ServeHTTP(rr, contactFaultRequest(t, index))
			assertContactFault(t, d, rr, 500)
		})
	}
}
func TestContactMutationsCheckAffectedRowAndMailbox(t *testing.T) {
	for _, index := range []int{2, 3, 4} {
		t.Run(contactFaultRoutes[index].name, func(t *testing.T) {
			d := &contactFaultDB{name: "Original", trust: "trusted", zeroWrite: true}
			rr := httptest.NewRecorder()
			contactFaultRouter(contactFaultGorm(t, d)).ServeHTTP(rr, contactFaultRequest(t, index))
			status := 404
			if index == 4 {
				status = 500
			}
			assertContactFault(t, d, rr, status)
			if !strings.Contains(d.writeSQL, `mailbox_id =`) || !strings.Contains(d.writeSQL, `id =`) {
				t.Fatalf("mutation lost mailbox/id guard: %s", d.writeSQL)
			}
		})
	}
}
func TestContactSuccessAndAbsenceStayStable(t *testing.T) {
	for index, route := range contactFaultRoutes {
		t.Run(route.name, func(t *testing.T) {
			d := &contactFaultDB{name: "Original", trust: "trusted"}
			rr := httptest.NewRecorder()
			contactFaultRouter(contactFaultGorm(t, d)).ServeHTTP(rr, contactFaultRequest(t, index))
			status := 200
			if index == 1 {
				status = 201
			}
			if index == 3 {
				status = 204
			}
			assertContactFault(t, d, rr, status)
			if index == 2 && (d.name != "Changed" || !strings.Contains(rr.Body.String(), `"name":"Changed"`)) {
				t.Fatalf("update did not return committed state: %s name=%s", rr.Body, d.name)
			}
			if index == 4 && (d.trust != "blocked" || !strings.Contains(rr.Body.String(), `"trust_level":"blocked"`)) {
				t.Fatalf("block did not return committed state: %s trust=%s", rr.Body, d.trust)
			}
		})
	}
	for _, index := range []int{2, 3, 4} {
		t.Run(contactFaultRoutes[index].name+"/missing", func(t *testing.T) {
			d := &contactFaultDB{missing: true}
			rr := httptest.NewRecorder()
			contactFaultRouter(contactFaultGorm(t, d)).ServeHTTP(rr, contactFaultRequest(t, index))
			status := 404
			if index == 4 {
				status = 201
			}
			assertContactFault(t, d, rr, status)
		})
	}
	for index := range contactFaultRoutes {
		t.Run(contactFaultRoutes[index].name+"/denied", func(t *testing.T) {
			d := &contactFaultDB{denied: true}
			rr := httptest.NewRecorder()
			contactFaultRouter(contactFaultGorm(t, d)).ServeHTTP(rr, contactFaultRequest(t, index))
			assertContactFault(t, d, rr, 403)
			if d.read != 1 || d.write != 0 || d.begin != 0 {
				t.Fatal("denied account reached contacts")
			}
		})
	}
}

func TestContactWritesAndSessionGrantsFollowCancellation(t *testing.T) {
	for _, index := range []int{2, 3, 4} {
		t.Run(contactFaultRoutes[index].name+"/write", func(t *testing.T) {
			d := &contactFaultDB{name: "Original", trust: "trusted", failWrite: true, wait: true, started: make(chan struct{})}
			r := contactFaultRequest(t, index)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			rr := httptest.NewRecorder()
			router := contactFaultRouter(contactFaultGorm(t, d))
			done := make(chan struct{})
			go func() { defer close(done); router.ServeHTTP(rr, r.WithContext(ctx)) }()
			select {
			case <-d.started:
			case <-time.After(time.Second):
				t.Fatal("write not reached")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("write ignored cancellation")
			}
			assertContactFault(t, d, rr, 500)
		})
	}
	for _, stage := range []int{2, 3} {
		t.Run("session grant read"+strconv.Itoa(stage), func(t *testing.T) {
			d := &contactFaultDB{failRead: stage, wait: true, started: make(chan struct{})}
			r := contactFaultRequest(t, 0)
			claims := &auth.Claims{AccountID: 7, Address: "reader@example.test"}
			r = r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, claims))
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			rr := httptest.NewRecorder()
			router := contactFaultRouter(contactFaultGorm(t, d))
			done := make(chan struct{})
			go func() { defer close(done); router.ServeHTTP(rr, r.WithContext(ctx)) }()
			select {
			case <-d.started:
			case <-time.After(time.Second):
				t.Fatal("grant stage not reached")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("grant ignored cancellation")
			}
			assertContactFault(t, d, rr, 500)
		})
	}
}
