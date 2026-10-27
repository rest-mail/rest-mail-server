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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The admin mailbox routes run their SQL with the HTTP request's context, and
// updating a mailbox keeps a storage failure a failure (#1164): a read that
// failed was answered 404 mailbox_not_found, and a read-back that failed after
// the change was ignored, answering 200 with the mailbox as it was before.

type mailboxContextKey struct{}

// mailboxDB answers mailbox reads with one row, or fails the read numbered
// failRead (1-based); writes succeed.
type mailboxDB struct {
	serviceProbeTestDB
	failRead int

	mu                 sync.Mutex
	reads              int
	seen, lost         []string
	commits, rollbacks int
}

func (d *mailboxDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *mailboxDB) Driver() driver.Driver                        { return d }
func (d *mailboxDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *mailboxDB) observe(ctx context.Context, q string) {
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(mailboxContextKey{}) != true {
		d.lost = append(d.lost, q)
	}
}

func (d *mailboxDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observe(ctx, q)
	if strings.HasPrefix(q, "SELECT count(") {
		return &deliveryReadRows{columns: []string{"count"}, values: []driver.Value{int64(1)}}, nil
	}
	if !strings.Contains(q, `FROM "mailboxes"`) {
		return &deliveryReadRows{}, nil
	}
	d.reads++
	if d.reads == d.failRead {
		return nil, errors.New("mailbox store lost at 10.0.0.9")
	}
	return &deliveryReadRows{columns: []string{"id", "address", "domain_id", "active", "display_name"},
		values: []driver.Value{int64(9), "someone@example.test", int64(3), true, "Someone"}}, nil
}

func (d *mailboxDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observe(ctx, q)
	return driver.RowsAffected(1), nil
}

func (d *mailboxDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observe(ctx, "BEGIN")
	return &mailboxTx{d}, nil
}

type mailboxTx struct{ d *mailboxDB }

func (tx *mailboxTx) Commit() error {
	tx.d.mu.Lock()
	tx.d.commits++
	tx.d.mu.Unlock()
	return nil
}
func (tx *mailboxTx) Rollback() error {
	tx.d.mu.Lock()
	tx.d.rollbacks++
	tx.d.mu.Unlock()
	return nil
}

func serveMailbox(t *testing.T, d *mailboxDB, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	rs := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	noop := func(string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
	}
	h := NewMailboxHandler(db, nil, nil)
	h.Register(rs, MailboxRouteChecks{OnList: noop, OnMailbox: noop})
	h.RegisterAddressCheck(rs)

	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), mailboxContextKey{}, true), 4*time.Second)
	t.Cleanup(cancel)
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r = withClaims(r, mailboxClaims(1, "admin@example.test"))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, r)
	return rr
}

func TestMailboxRoutesCarryTheRequestContext(t *testing.T) {
	for _, route := range []struct{ name, method, path, body string }{
		{"listMailboxes", http.MethodGet, "/api/v1/admin/mailboxes", ""},
		{"getMailbox", http.MethodGet, "/api/v1/admin/mailboxes/9", ""},
		{"updateMailbox", http.MethodPatch, "/api/v1/admin/mailboxes/9", `{"display_name":"Renamed"}`},
		{"checkMailboxAddress", http.MethodGet, "/api/mailboxes?address=someone@example.test", ""},
	} {
		t.Run(route.name, func(t *testing.T) {
			d := &mailboxDB{}
			rr := serveMailbox(t, d, route.method, route.path, route.body)
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d (%s), want 200", rr.Code, rr.Body.String())
			}
			if len(d.lost) > 0 {
				t.Errorf("%d of %d statements ran without the request's context, first %q", len(d.lost), len(d.seen), d.lost[0])
			}
		})
	}
}

func TestUpdateMailboxKeepsAStorageFailure(t *testing.T) {
	// The first read failing is not a missing mailbox.
	failedRead := &mailboxDB{failRead: 1}
	rr := serveMailbox(t, failedRead, http.MethodPatch, "/api/v1/admin/mailboxes/9", `{"display_name":"Renamed"}`)
	if rr.Code != http.StatusInternalServerError || errorCode(rr) != "internal_error" {
		t.Errorf("failed read: %d %s, want 500 internal_error", rr.Code, rr.Body.String())
	}

	// The read-back failing after the change undoes the change.
	failedReload := &mailboxDB{failRead: 2}
	rr = serveMailbox(t, failedReload, http.MethodPatch, "/api/v1/admin/mailboxes/9", `{"display_name":"Renamed"}`)
	if rr.Code != http.StatusInternalServerError || errorCode(rr) != "internal_error" {
		t.Errorf("failed read-back: %d %s, want 500 internal_error", rr.Code, rr.Body.String())
	}
	if failedReload.commits != 0 || failedReload.rollbacks == 0 {
		t.Errorf("failed read-back: %d commits, %d rollbacks; the change must not commit", failedReload.commits, failedReload.rollbacks)
	}
	for _, body := range []string{rr.Body.String()} {
		if strings.Contains(body, "10.0.0.9") {
			t.Errorf("the dependency failure reached the client: %s", body)
		}
	}

	// A PATCH that changes nothing still reads the mailbox back, and a failed
	// read-back is a failure there too.
	unchanged := &mailboxDB{failRead: 2}
	if rr := serveMailbox(t, unchanged, http.MethodPatch, "/api/v1/admin/mailboxes/9", `{}`); rr.Code != http.StatusInternalServerError {
		t.Errorf("failed read-back of an unchanged mailbox: %d %s, want 500", rr.Code, rr.Body.String())
	}
}
