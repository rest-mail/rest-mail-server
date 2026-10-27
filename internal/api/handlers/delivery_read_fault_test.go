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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These fixtures exercise actual GORM SQL through database/sql. They need no
// running PostgreSQL and reject unexpected reads and every attempted write.
type deliveryReadStep struct {
	table   string
	columns []string
	values  []driver.Value
	err     error
	wait    bool
}
type deliveryReadDB struct {
	serviceProbeTestDB
	steps                      []deliveryReadStep
	reads, writes, lostContext int
	started                    chan struct{}
}
type deliveryContextKey struct{}

func (d *deliveryReadDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *deliveryReadDB) Driver() driver.Driver                        { return d }
func (d *deliveryReadDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *deliveryReadDB) Begin() (driver.Tx, error) {
	d.writes++
	return nil, errors.New("unexpected delivery transaction")
}
func (d *deliveryReadDB) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	d.writes++
	return nil, errors.New("unexpected delivery write")
}
func (d *deliveryReadDB) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.HasPrefix(query, "SELECT ") {
		d.writes++
		return nil, errors.New("unexpected delivery write")
	}
	d.reads++
	if _, ok := ctx.Deadline(); !ok || ctx.Value(deliveryContextKey{}) != true {
		d.lostContext++
	}
	if d.reads > len(d.steps) {
		return nil, errors.New("unexpected delivery read")
	}
	step := d.steps[d.reads-1]
	if !strings.Contains(query, `FROM "`+step.table+`"`) {
		return nil, errors.New("unexpected delivery table")
	}
	if step.wait {
		close(d.started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
			return nil, errors.New("HTTP cancellation lost")
		}
	}
	if step.err != nil {
		return nil, step.err
	}
	return &deliveryReadRows{columns: step.columns, values: step.values}, nil
}

type deliveryReadRows struct {
	columns []string
	values  []driver.Value
	done    bool
}

func (r *deliveryReadRows) Columns() []string { return r.columns }
func (r *deliveryReadRows) Close() error      { return nil }
func (r *deliveryReadRows) Next(dest []driver.Value) error {
	if r.done || r.values == nil {
		return io.EOF
	}
	r.done = true
	copy(dest, r.values)
	return nil
}
func deliveryReadGorm(t *testing.T, d *deliveryReadDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func deliveryDomainStep() deliveryReadStep {
	return deliveryReadStep{table: "domains", columns: []string{"id", "name", "active"}, values: []driver.Value{int64(1), "example.test", true}}
}
func deliveryMailboxStep(address string) deliveryReadStep {
	return deliveryReadStep{table: "mailboxes", columns: []string{"id", "domain_id", "address", "active"}, values: []driver.Value{int64(2), int64(1), address, true}}
}
func deliveryPipelineStep(filters string) deliveryReadStep {
	return deliveryReadStep{table: "pipelines", columns: []string{"id", "domain_id", "direction", "active", "filters"}, values: []driver.Value{int64(3), int64(1), "inbound", true, []byte(filters)}}
}
func deliveryRequest(t *testing.T, body, path string) *http.Request {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), deliveryContextKey{}, true), 3*time.Second)
	t.Cleanup(cancel)
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.0.2.10:1234"
	return r
}
func gatewayFaultRoutes(db *gorm.DB) http.Handler {
	router := chi.NewRouter()
	NewMessageHandler(db, nil, nil, "", nil).RegisterDelivery(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }})
	return router
}
func assertDeliveryReadRefusal(t *testing.T, d *deliveryReadDB, rr *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rr.Code != status || !strings.Contains(rr.Body.String(), `"code":"`+code+`"`) || strings.Contains(rr.Body.String(), "private") || d.writes != 0 || d.reads != len(d.steps) || d.lostContext != 0 {
		t.Fatalf("status=%d body=%s reads=%d/%d writes=%d lost HTTP contexts=%d", rr.Code, rr.Body, d.reads, len(d.steps), d.writes, d.lostContext)
	}
}
func TestRESTMAILDeliveryReadFaultsDeferBeforeSideEffects(t *testing.T) {
	base := []deliveryReadStep{deliveryDomainStep(), deliveryMailboxStep("one@example.test"), deliveryMailboxStep("two@example.test"), deliveryDomainStep(), deliveryPipelineStep("[]")}
	for index, name := range []string{"sender identity", "first recipient", "later recipient", "recipient domain", "pipeline configuration"} {
		t.Run(name, func(t *testing.T) {
			steps := append([]deliveryReadStep(nil), base[:index+1]...)
			steps[index].err = errors.New("private dependency failure")
			d := &deliveryReadDB{steps: steps}
			h := NewRestmailHandler(deliveryReadGorm(t, d), nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}})
			rr := httptest.NewRecorder()
			restmailRoutes(h).ServeHTTP(rr, deliveryRequest(t, `{"from":"sender@example.test","to":["one@example.test","two@example.test"]}`, restmailMessagesPath))
			assertDeliveryReadRefusal(t, d, rr, 503, "deferred")
		})
	}
	t.Run("corrupt stored pipeline", func(t *testing.T) {
		steps := append([]deliveryReadStep(nil), base...)
		steps[4] = deliveryPipelineStep(`{"not":"a filter list"}`)
		d := &deliveryReadDB{steps: steps}
		h := NewRestmailHandler(deliveryReadGorm(t, d), nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}})
		rr := httptest.NewRecorder()
		restmailRoutes(h).ServeHTTP(rr, deliveryRequest(t, `{"from":"sender@example.test","to":["one@example.test","two@example.test"]}`, restmailMessagesPath))
		assertDeliveryReadRefusal(t, d, rr, 503, "deferred")
	})
}
func TestGatewayDeliveryLookupFaultsAreNotMissingRecipients(t *testing.T) {
	for _, body := range []string{`{"mailbox_id":2}`, `{"address":"one@example.test"}`} {
		t.Run(body, func(t *testing.T) {
			d := &deliveryReadDB{steps: []deliveryReadStep{{table: "mailboxes", err: errors.New("private lookup failure")}}}
			rr := httptest.NewRecorder()
			gatewayFaultRoutes(deliveryReadGorm(t, d)).ServeHTTP(rr, deliveryRequest(t, body, "/api/v1/messages/deliver"))
			assertDeliveryReadRefusal(t, d, rr, 500, "internal_error")
		})
	}
}
func TestGatewayDeliveryMissingRecipientKeepsNotFound(t *testing.T) {
	for _, body := range []string{`{"mailbox_id":2}`, `{"address":"missing@example.test"}`} {
		t.Run(body, func(t *testing.T) {
			d := &deliveryReadDB{steps: []deliveryReadStep{{table: "mailboxes", columns: []string{"id"}}}}
			rr := httptest.NewRecorder()
			gatewayFaultRoutes(deliveryReadGorm(t, d)).ServeHTTP(rr, deliveryRequest(t, body, "/api/v1/messages/deliver"))
			assertDeliveryReadRefusal(t, d, rr, 404, "mailbox_not_found")
		})
	}
}
func TestDeliveryReadCancellationReachesDatabase(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		t.Run(map[bool]string{false: "RESTMAIL", true: "gateway"}[gateway], func(t *testing.T) {
			table := "domains"
			body := `{"from":"sender@example.test","to":["one@example.test"]}`
			path := restmailMessagesPath
			status, code := 503, "deferred"
			if gateway {
				table = "mailboxes"
				body = `{"mailbox_id":2}`
				path = "/api/v1/messages/deliver"
				status, code = 500, "internal_error"
			}
			d := &deliveryReadDB{steps: []deliveryReadStep{{table: table, wait: true}}, started: make(chan struct{})}
			db := deliveryReadGorm(t, d)
			h := restmailRoutes(NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}}))
			if gateway {
				h = gatewayFaultRoutes(db)
			}
			r := deliveryRequest(t, body, path)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			rr := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); h.ServeHTTP(rr, r.WithContext(ctx)) }()
			select {
			case <-d.started:
				cancel()
			case <-time.After(time.Second):
				t.Error("database read never started")
				cancel()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("handler ignored cancellation")
			}
			assertDeliveryReadRefusal(t, d, rr, status, code)
		})
	}
}

// In non-strict mode only a confirmed external sender may skip alignment. A
// failed domain query cannot establish that a potentially local sender is external.
func TestRESTMAILUntrustedSenderLookupFaultDefers(t *testing.T) {
	d := &deliveryReadDB{steps: []deliveryReadStep{{table: "domains", err: errors.New("private domain lookup failed")}}}
	h := restmailRoutes(NewRestmailHandler(deliveryReadGorm(t, d), nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, deliveryRequest(t, `{"from":"sender@example.test","to":["one@example.test"]}`, restmailMessagesPath))
	assertDeliveryReadRefusal(t, d, rr, 503, "deferred")
}
func TestRESTMAILRecipientResolutionRetainsErrorsAndIdentity(t *testing.T) {
	missing := deliveryReadStep{table: "mailboxes", columns: []string{"id"}}
	count := func(n int64) deliveryReadStep {
		return deliveryReadStep{table: "aliases", columns: []string{"count"}, values: []driver.Value{n}}
	}
	sources := deliveryReadStep{table: "aliases", columns: []string{"source_address"}}
	fault := errors.New("private recipient lookup failed")
	inactive := deliveryMailboxStep("one+tag@example.test")
	inactive.values[3] = false
	for _, tc := range []struct {
		name                         string
		steps                        []deliveryReadStep
		wantError                    error
		wantAddress, wantDeliveredTo string
	}{
		{"exact identity wins", []deliveryReadStep{deliveryMailboxStep("one+tag@example.test")}, nil, "one+tag@example.test", ""},
		{"plus fallback", []deliveryReadStep{missing, count(0), sources, deliveryMailboxStep("one@example.test")}, nil, "one@example.test", "one+tag@example.test"},
		{"inactive identity never falls back", []deliveryReadStep{inactive}, gorm.ErrRecordNotFound, "", ""},
		{"explicit alias reserves full identity", []deliveryReadStep{missing, count(1)}, gorm.ErrRecordNotFound, "", ""},
		{"exact lookup fault", []deliveryReadStep{{table: "mailboxes", err: fault}}, fault, "", ""},
		{"alias count fault", []deliveryReadStep{missing, {table: "aliases", err: fault}}, fault, "", ""},
		{"legacy alias lookup fault", []deliveryReadStep{missing, count(0), {table: "aliases", err: fault}}, fault, "", ""},
		{"base lookup fault", []deliveryReadStep{missing, count(0), sources, {table: "mailboxes", err: fault}}, fault, "", ""},
		{"base absent", []deliveryReadStep{missing, count(0), sources, missing}, gorm.ErrRecordNotFound, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &deliveryReadDB{steps: tc.steps}
			h := NewRestmailHandler(deliveryReadGorm(t, d), nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
			r := deliveryRequest(t, "{}", restmailMessagesPath)
			mb, deliveredTo, err := h.resolveRecipientMailbox(r.Context(), "one+tag@example.test")
			if !errors.Is(err, tc.wantError) || mb.Address != tc.wantAddress || deliveredTo != tc.wantDeliveredTo || d.reads != len(tc.steps) || d.writes != 0 || d.lostContext != 0 {
				t.Fatalf("mailbox=%+v deliveredTo=%q error=%v reads=%d writes=%d lost=%d", mb, deliveredTo, err, d.reads, d.writes, d.lostContext)
			}
		})
	}
}
func TestRESTMAILPipelineDefaultRequiresAbsentConfiguration(t *testing.T) {
	missingDomain := deliveryReadStep{table: "domains", columns: []string{"id"}}
	missingPipeline := deliveryReadStep{table: "pipelines", columns: []string{"id"}}
	for _, tc := range []struct {
		name                 string
		steps                []deliveryReadStep
		wantNil, wantDefault bool
	}{
		{"absent domain", []deliveryReadStep{missingDomain}, true, false},
		{"absent pipeline", []deliveryReadStep{deliveryDomainStep(), missingPipeline}, false, true},
		{"configured empty filters", []deliveryReadStep{deliveryDomainStep(), deliveryPipelineStep("[]")}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &deliveryReadDB{steps: tc.steps}
			h := NewRestmailHandler(deliveryReadGorm(t, d), nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
			r := deliveryRequest(t, "{}", restmailMessagesPath)
			cfg, err := h.inboundPipeline(r.Context(), "example.test")
			if err != nil || (cfg == nil) != tc.wantNil || d.reads != len(tc.steps) || d.writes != 0 || d.lostContext != 0 {
				t.Fatalf("config=%+v error=%v reads=%d writes=%d lost=%d", cfg, err, d.reads, d.writes, d.lostContext)
			}
			if cfg != nil && (cfg.DomainID != 1 || cfg.Direction != "inbound" || !cfg.Active || (len(cfg.Filters) > 0) != tc.wantDefault) {
				t.Fatalf("pipeline changed: %+v", cfg)
			}
		})
	}
}
