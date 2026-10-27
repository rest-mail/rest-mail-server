package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A message the pipeline holds is stored in the quarantine, or the delivery
// fails so that the sender retries (#1161, #1163). The hold's insert error was
// ignored on both inbound paths: the message was answered as taken, and was in
// neither the quarantine nor the mailbox.

// holdAll is an action filter that quarantines every message.
type holdAll struct{}

func (holdAll) Name() string              { return "hold_all" }
func (holdAll) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (holdAll) Execute(context.Context, *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionQuarantine}, nil
}

func holdAllEngine() *pipeline.Engine {
	registry := pipeline.NewRegistry()
	registry.Register("hold_all", func([]byte) (pipeline.Filter, error) { return holdAll{}, nil })
	return pipeline.NewEngine(registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// holdDB answers the reads a delivery makes, with one active domain, mailbox
// and inbound pipeline that runs hold_all, and fails the quarantine insert
// when told to.
type holdDB struct {
	serviceProbeTestDB
	failHold bool

	mu                 sync.Mutex
	holds, otherWrites int
	commits            int
}

func (d *holdDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *holdDB) Driver() driver.Driver                        { return d }
func (d *holdDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *holdDB) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case strings.HasPrefix(q, `INSERT INTO "quarantine"`):
		d.holds++
		if d.failHold {
			return nil, errors.New("private quarantine store failure")
		}
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(90)}}, nil
	case strings.HasPrefix(q, "INSERT"):
		d.otherWrites++
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(91)}}, nil
	case strings.Contains(q, `FROM "domains"`):
		return &deliveryReadRows{columns: []string{"id", "name", "active"}, values: []driver.Value{int64(1), "example.test", true}}, nil
	case strings.Contains(q, `FROM "mailboxes"`):
		address := "one@example.test"
		for _, a := range args {
			if s, ok := a.Value.(string); ok && strings.Contains(s, "@") {
				address = s
			}
		}
		return &deliveryReadRows{columns: []string{"id", "domain_id", "address", "active"},
			values: []driver.Value{int64(2), int64(1), address, true}}, nil
	case strings.Contains(q, `FROM "pipelines"`):
		return &deliveryReadRows{columns: []string{"id", "domain_id", "direction", "active", "filters"},
			values: []driver.Value{int64(3), int64(1), "inbound", true,
				[]byte(`[{"name":"hold_all","type":"action","enabled":true}]`)}}, nil
	}
	return &deliveryReadRows{}, nil
}

func (d *holdDB) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.otherWrites++
	return driver.RowsAffected(1), nil
}

func (d *holdDB) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &holdTx{d}, nil
}

type holdTx struct{ d *holdDB }

func (tx *holdTx) Commit() error {
	tx.d.mu.Lock()
	tx.d.commits++
	tx.d.mu.Unlock()
	return nil
}
func (tx *holdTx) Rollback() error { return nil }

func holdGorm(t *testing.T, d *holdDB) *gorm.DB {
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

func TestRESTMAILDeliveryDefersWhenTheHoldCannotBeStored(t *testing.T) {
	deliver := func(d *holdDB) *httptest.ResponseRecorder {
		h := NewRestmailHandler(holdGorm(t, d), holdAllEngine(), nil, RestmailTarpitConfig{},
			RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}})
		rr := httptest.NewRecorder()
		restmailRoutes(h).ServeHTTP(rr, deliveryRequest(t,
			`{"from":"sender@example.test","to":["one@example.test","two@example.test"],"subject":"held","body_text":"x"}`,
			restmailMessagesPath))
		return rr
	}

	stored := &holdDB{}
	if rr := deliver(stored); rr.Code >= 500 || stored.holds == 0 || stored.commits == 0 {
		t.Fatalf("a stored hold: %d %s, %d holds, %d commits", rr.Code, rr.Body.String(), stored.holds, stored.commits)
	}

	failed := &holdDB{failHold: true}
	rr := deliver(failed)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"code":"deferred"`) {
		t.Fatalf("a hold that could not be stored: %d %s, want 503 deferred", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "private") || failed.commits != 0 {
		t.Errorf("body %s, %d commits", rr.Body.String(), failed.commits)
	}
}

func TestGatewayDeliveryFailsWhenTheHoldCannotBeStored(t *testing.T) {
	deliver := func(d *holdDB) *httptest.ResponseRecorder {
		router := chi.NewRouter()
		NewMessageHandler(holdGorm(t, d), nil, holdAllEngine(), "", nil).RegisterDelivery(
			endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()},
			endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }})
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, deliveryRequest(t,
			`{"mailbox_id":2,"sender":"sender@remote.example","subject":"held","body_text":"x"}`, "/api/v1/messages/deliver"))
		return rr
	}

	stored := &holdDB{}
	if rr := deliver(stored); rr.Code >= 500 || stored.holds != 1 {
		t.Fatalf("a stored hold: %d %s, %d holds", rr.Code, rr.Body.String(), stored.holds)
	}

	failed := &holdDB{failHold: true}
	rr := deliver(failed)
	if rr.Code != http.StatusInternalServerError || strings.Contains(rr.Body.String(), "private") {
		t.Fatalf("a hold that could not be stored: %d %s, want a sanitized 500 the gateway retries", rr.Code, rr.Body.String())
	}
}
