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
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type draftFaultContextKey struct{}
type draftFaultDB struct {
	serviceProbeTestDB
	mu                                          sync.Mutex
	read, write, begin, commit, rollback, lost  int
	failRead                                    int
	failWrite, zeroWrite, missing, denied, wait bool
	started                                     chan struct{}
	subject, pending                            string
	writeSQL                                    string
	writeArgs                                   []driver.NamedValue
}

func (d *draftFaultDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *draftFaultDB) Driver() driver.Driver                        { return d }
func (d *draftFaultDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *draftFaultDB) observe(ctx context.Context) {
	if _, ok := ctx.Deadline(); !ok || ctx.Value(draftFaultContextKey{}) != true {
		d.lost++
	}
}
func (d *draftFaultDB) fault(ctx context.Context) error {
	if d.wait {
		close(d.started)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return errors.New("HTTP cancellation lost")
		}
	}
	return errors.New("private draft database failure")
}
func (d *draftFaultDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.read++
	d.observe(ctx)
	if d.failRead == d.read {
		return nil, d.fault(ctx)
	}
	if strings.Contains(q, `FROM "messages"`) {
		cols := []string{"id", "mailbox_id", "sender", "subject", "is_draft", "recipients_to", "recipients_cc"}
		if d.missing {
			return &draftFaultRows{cols: cols}, nil
		}
		mailbox := int64(1)
		if d.denied {
			mailbox = 2
		}
		subject := d.subject
		if d.pending != "" {
			subject = d.pending
		}
		return &draftFaultRows{cols: cols, values: []driver.Value{int64(5), mailbox, "owner@example.test", subject, true, []byte(`["recipient@example.test"]`), []byte(`[]`)}}, nil
	}
	if strings.Contains(q, `FROM "mailboxes"`) {
		id := int64(1)
		if d.denied {
			id = 2
		}
		return &draftFaultRows{cols: []string{"id", "address", "active"}, values: []driver.Value{id, "owner@example.test", true}}, nil
	}
	if strings.Contains(q, `FROM "scheduled_sends"`) {
		cols := []string{"id", "account_id", "from", "status"}
		if d.missing {
			return &draftFaultRows{cols: cols}, nil
		}
		return &draftFaultRows{cols: cols, values: []driver.Value{int64(5), int64(1), "owner@example.test", "pending"}}, nil
	}
	return nil, errors.New("unexpected draft SQL")
}
func (d *draftFaultDB) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.write++
	d.writeSQL = q
	d.writeArgs = append([]driver.NamedValue(nil), args...)
	d.observe(ctx)
	if d.failWrite {
		return nil, d.fault(ctx)
	}
	if d.zeroWrite {
		return driver.RowsAffected(0), nil
	}
	d.pending = "changed"
	return driver.RowsAffected(1), nil
}
func (d *draftFaultDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observe(ctx)
	d.begin++
	return &draftFaultTx{d}, nil
}

type draftFaultTx struct{ d *draftFaultDB }

func (tx *draftFaultTx) Commit() error {
	tx.d.mu.Lock()
	defer tx.d.mu.Unlock()
	tx.d.commit++
	if tx.d.pending != "" {
		tx.d.subject = tx.d.pending
	}
	tx.d.pending = ""
	return nil
}
func (tx *draftFaultTx) Rollback() error {
	tx.d.mu.Lock()
	defer tx.d.mu.Unlock()
	tx.d.rollback++
	tx.d.pending = ""
	return nil
}

type draftFaultRows struct {
	cols   []string
	values []driver.Value
	done   bool
}

func (r *draftFaultRows) Columns() []string { return r.cols }
func (r *draftFaultRows) Close() error      { return nil }
func (r *draftFaultRows) Next(out []driver.Value) error {
	if r.done || r.values == nil {
		return io.EOF
	}
	r.done = true
	copy(out, r.values)
	return nil
}
func draftFaultGorm(t *testing.T, d *draftFaultDB) *gorm.DB {
	t.Helper()
	d.subject = "original"
	pool := sql.OpenDB(d)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

var draftFaultRoutes = []struct{ name, method, path, body string }{
	{"save", http.MethodPost, "/api/v1/messages/draft", `{"from":"owner@example.test"}`},
	{"update", http.MethodPut, "/api/v1/messages/draft/5", `{"subject":"changed"}`},
	{"send preflight", http.MethodPost, "/api/v1/messages/draft/5/send", ``},
	{"scheduled list", http.MethodGet, "/api/v1/messages/scheduled", ``},
	{"scheduled cancellation", http.MethodDelete, "/api/v1/messages/scheduled/5", ``},
}

func draftFaultRequest(t *testing.T, index int) *http.Request {
	t.Helper()
	route := draftFaultRoutes[index]
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), draftFaultContextKey{}, true), 3*time.Second)
	t.Cleanup(cancel)
	ctx = context.WithValue(ctx, middleware.ClaimsKey, &auth.Claims{AccountID: 1, CredentialID: 1, CredentialKind: "app_password"})
	r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	return r
}
func assertDraftFault(t *testing.T, d *draftFaultDB, rr *httptest.ResponseRecorder, status int) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if rr.Code != status || strings.Contains(rr.Body.String(), "private") || d.lost != 0 {
		t.Fatalf("status=%d body=%s SQL reads=%d writes=%d commits=%d lost HTTP contexts=%d", rr.Code, rr.Body, d.read, d.write, d.commit, d.lost)
	}
	if status >= 400 && d.subject != "original" {
		t.Fatalf("failed update changed stored draft to %q", d.subject)
	}
}
func TestDraftAndScheduledReadFailuresRetainErrors(t *testing.T) {
	for index, route := range draftFaultRoutes {
		for _, read := range []int{1, 2} {
			if index == 0 && read == 2 {
				continue
			}
			t.Run(route.name+string(rune('0'+read)), func(t *testing.T) {
				d := &draftFaultDB{failRead: read}
				h := composeRoutes(NewMessageHandler(draftFaultGorm(t, d), nil, nil, "", nil))
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, draftFaultRequest(t, index))
				assertDraftFault(t, d, rr, 500)
			})
		}
	}
}
func TestDraftUpdateWriteAndReloadAreAtomic(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		failWrite, zeroWrite bool
		failRead, status     int
	}{
		{"update failure", true, false, 0, 500}, {"reload failure", false, false, 3, 500}, {"draft disappeared", false, true, 0, 404}, {"success", false, false, 0, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &draftFaultDB{failRead: tc.failRead, failWrite: tc.failWrite, zeroWrite: tc.zeroWrite}
			h := composeRoutes(NewMessageHandler(draftFaultGorm(t, d), nil, nil, "", nil))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, draftFaultRequest(t, 1))
			assertDraftFault(t, d, rr, tc.status)
			d.mu.Lock()
			defer d.mu.Unlock()
			if tc.status == 200 && (d.subject != "changed" || d.commit != 1 || !strings.Contains(rr.Body.String(), `"subject":"changed"`)) {
				t.Fatal("successful update was not committed and returned")
			}
			if tc.status != 200 && d.commit != 0 {
				t.Fatal("failed draft update committed")
			}
		})
	}
}
func TestDraftAndScheduledCancellationReachesSQL(t *testing.T) {
	for index, route := range draftFaultRoutes {
		t.Run(route.name, func(t *testing.T) {
			d := &draftFaultDB{failRead: 1, wait: true, started: make(chan struct{})}
			h := composeRoutes(NewMessageHandler(draftFaultGorm(t, d), nil, nil, "", nil))
			rr := httptest.NewRecorder()
			r := draftFaultRequest(t, index)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); h.ServeHTTP(rr, r.WithContext(ctx)) }()
			select {
			case <-d.started:
				cancel()
			case <-time.After(time.Second):
				t.Error("SQL did not start")
				cancel()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled operation did not return")
			}
			assertDraftFault(t, d, rr, 500)
		})
	}
}
func TestDraftMissingAndDeniedResponsesStayUnchanged(t *testing.T) {
	for _, index := range []int{1, 2, 3, 4} {
		t.Run(draftFaultRoutes[index].name, func(t *testing.T) {
			d := &draftFaultDB{missing: true}
			h := composeRoutes(NewMessageHandler(draftFaultGorm(t, d), nil, nil, "", nil))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, draftFaultRequest(t, index))
			want := 404
			if index == 3 {
				want = 200
			}
			assertDraftFault(t, d, rr, want)
		})
	}
	for _, index := range []int{1, 2} {
		t.Run(draftFaultRoutes[index].name+" denied", func(t *testing.T) {
			d := &draftFaultDB{denied: true}
			h := composeRoutes(NewMessageHandler(draftFaultGorm(t, d), nil, nil, "", nil))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, draftFaultRequest(t, index))
			assertDraftFault(t, d, rr, 403)
		})
	}
}

func TestDraftUpdateCancellationRollsBackWriteAndReload(t *testing.T) {
	for _, reload := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "reload"}[reload], func(t *testing.T) {
			d := &draftFaultDB{wait: true, started: make(chan struct{})}
			if reload {
				d.failRead = 3
			} else {
				d.failWrite = true
			}
			h := composeRoutes(NewMessageHandler(draftFaultGorm(t, d), nil, nil, "", nil))
			rr := httptest.NewRecorder()
			r := draftFaultRequest(t, 1)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); h.ServeHTTP(rr, r.WithContext(ctx)) }()
			select {
			case <-d.started:
				cancel()
			case <-time.After(time.Second):
				t.Error("transactional SQL did not start")
				cancel()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("transactional SQL ignored cancellation")
			}
			assertDraftFault(t, d, rr, 500)
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.commit != 0 {
				t.Fatal("cancelled draft update committed")
			}
		})
	}
}

func TestDraftUpdateDeclaredThreadHeadersAreWritten(t *testing.T) {
	for _, tc := range []struct {
		name, body, reply, refs string
		wantWrite               bool
	}{
		{"set", `{"in_reply_to":"<parent@example.test>","references":"<root@example.test>"}`, "<parent@example.test>", "<root@example.test>", true},
		{"clear", `{"in_reply_to":"","references":""}`, "", "", true},
		{"omit", `{}`, "", "", false}, {"null", `{"in_reply_to":null,"references":null}`, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &draftFaultDB{}
			h := composeRoutes(NewMessageHandler(draftFaultGorm(t, d), nil, nil, "", nil))
			r := draftFaultRequest(t, 1)
			r.Body = io.NopCloser(strings.NewReader(tc.body))
			r.ContentLength = int64(len(tc.body))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)
			if rr.Code != 200 {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if tc.wantWrite {
				if !strings.Contains(d.writeSQL, `"in_reply_to"`) || !strings.Contains(d.writeSQL, `"references"`) {
					t.Fatalf("declared thread fields were not written: %s", d.writeSQL)
				}
				foundReply, foundRefs := false, false
				for _, a := range d.writeArgs {
					if a.Value == tc.reply {
						foundReply = true
					}
					if a.Value == tc.refs {
						foundRefs = true
					}
				}
				if !foundReply || !foundRefs {
					t.Fatal("thread values did not reach SQL")
				}
			} else if d.write != 0 {
				t.Fatal("omitted/null thread fields caused a write")
			}
		})
	}
}
