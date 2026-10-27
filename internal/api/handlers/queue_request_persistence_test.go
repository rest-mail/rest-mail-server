package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These fixtures use database/sql's real context and transaction machinery,
// without opening a database or replacing the registered HTTP handlers.
type queueRequestKey struct{}

type queueRequestDB struct {
	serviceProbeTestDB
	stage          string
	status         string
	missing        bool
	zeroAffected   bool
	beginCalls     int
	queries        int
	writes         int
	lockReads      int
	contextMissing bool
	active         atomic.Bool
	commits        atomic.Int32
	rollbacks      atomic.Int32
	statements     []string
	arguments      [][]driver.NamedValue
}

func (c *queueRequestDB) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *queueRequestDB) Driver() driver.Driver                        { return c }
func (c *queueRequestDB) Open(string) (driver.Conn, error)             { return c, nil }

func (c *queueRequestDB) observe(ctx context.Context) {
	_, deadline := ctx.Deadline()
	if !deadline || ctx.Value(queueRequestKey{}) != "queue request" {
		c.contextMissing = true
	}
}

func (c *queueRequestDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	c.observe(ctx)
	c.beginCalls++
	if c.stage == "begin" {
		return nil, errors.New("private queue begin failure")
	}
	c.active.Store(true)
	return queueRequestTx{c}, nil
}

type queueRequestTx struct{ c *queueRequestDB }

func (tx queueRequestTx) Commit() error {
	tx.c.active.Store(false)
	tx.c.commits.Add(1)
	if tx.c.stage == "commit" {
		return errors.New("private queue commit failure")
	}
	return nil
}

func (tx queueRequestTx) Rollback() error {
	tx.c.active.Store(false)
	tx.c.rollbacks.Add(1)
	return nil
}

func (c *queueRequestDB) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.observe(ctx)
	c.queries++
	c.statements = append(c.statements, query)
	c.arguments = append(c.arguments, args)
	if strings.Contains(query, "FOR UPDATE") && c.active.Load() {
		c.lockReads++
	}
	if c.stage == "read" || (c.stage == "list" && c.queries == 2) {
		return nil, errors.New("private queue read failure")
	}
	if c.stage == "wait-read" {
		return nil, waitQueueContext(ctx)
	}
	if strings.Contains(query, "count(*)") && !strings.Contains(query, "GROUP BY") {
		return &queueRequestRows{columns: []string{"count"}, values: [][]driver.Value{{int64(0)}}}, nil
	}
	if strings.Contains(query, "GROUP BY") || !strings.Contains(query, "LIMIT") || !strings.Contains(query, `"id" =`) {
		return &queueRequestRows{columns: []string{"id", "status"}}, nil
	}
	rows := &queueRequestRows{columns: []string{"id", "status"}}
	if !c.missing {
		status := c.status
		if status == "" {
			status = "deferred"
		}
		rows.values = [][]driver.Value{{int64(1), status}}
	}
	return rows, nil
}

func (c *queueRequestDB) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.observe(ctx)
	c.writes++
	c.statements = append(c.statements, query)
	c.arguments = append(c.arguments, args)
	if c.stage == "write" {
		return nil, errors.New("private queue write failure")
	}
	if c.stage == "wait-write" {
		return nil, waitQueueContext(ctx)
	}
	if c.zeroAffected {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

func waitQueueContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return errors.New("request cancellation did not reach queue SQL")
	}
}

type queueRequestRows struct {
	columns []string
	values  [][]driver.Value
}

func (r *queueRequestRows) Columns() []string { return r.columns }
func (r *queueRequestRows) Close() error      { return nil }
func (r *queueRequestRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

func queueRequestGorm(t *testing.T, c *queueRequestDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(c)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

var queueRequestOperations = []struct{ method, path, body string }{
	{"GET", "/api/v1/admin/queue", ""},
	{"GET", "/api/v1/admin/queue/stats", ""},
	{"GET", "/api/v1/admin/queue/1", ""},
	{"POST", "/api/v1/admin/queue/1/retry", ""},
	{"POST", "/api/v1/admin/queue/1/bounce", ""},
	{"DELETE", "/api/v1/admin/queue/1", ""},
	{"POST", "/api/v1/admin/queue/bulk-retry", `{"ids":[1]}`},
	{"POST", "/api/v1/admin/queue/bulk-bounce", `{"ids":[1]}`},
	{"DELETE", "/api/v1/admin/queue/bulk-delete", `{"ids":[1]}`},
}

func serveQueueRequest(t *testing.T, c *queueRequestDB, method, path, body string, timeout time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	ctx := context.WithValue(t.Context(), queueRequestKey{}, "queue request")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	queueRoutes(NewQueueHandler(queueRequestGorm(t, c))).ServeHTTP(rr, r)
	return rr
}

func TestQueueRequestContextReachesEveryOperation(t *testing.T) {
	for _, op := range queueRequestOperations {
		t.Run(op.method+op.path, func(t *testing.T) {
			c := &queueRequestDB{}
			rr := serveQueueRequest(t, c, op.method, op.path, op.body, time.Second)
			want := 200
			if op.method == "DELETE" && op.body == "" {
				want = 204
			}
			if rr.Code != want || c.contextMissing || c.queries+c.writes == 0 {
				t.Fatalf("status=%d contextMissing=%t queries=%d writes=%d body=%s", rr.Code, c.contextMissing, c.queries, c.writes, rr.Body)
			}
		})
	}
}

func TestQueueRequestCancellationStopsSQL(t *testing.T) {
	for _, op := range queueRequestOperations {
		t.Run(op.method+op.path, func(t *testing.T) {
			stage := "wait-write"
			if op.method == "GET" || strings.HasSuffix(op.path, "/retry") || strings.HasSuffix(op.path, "/bounce") {
				stage = "wait-read"
			}
			c := &queueRequestDB{stage: stage}
			start := time.Now()
			rr := serveQueueRequest(t, c, op.method, op.path, op.body, 20*time.Millisecond)
			if rr.Code != 500 || time.Since(start) > time.Second || c.contextMissing || c.commits.Load() != 0 || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("status=%d elapsed=%s contextMissing=%t commits=%d body=%s", rr.Code, time.Since(start), c.contextMissing, c.commits.Load(), rr.Body)
			}
		})
	}
}

func TestQueueRequestStorageFailuresAreSanitized(t *testing.T) {
	for _, op := range queueRequestOperations {
		stages := []string{"begin", "write", "commit"}
		if op.method == "GET" {
			stages = []string{"read"}
			if op.path == "/api/v1/admin/queue" {
				stages = append(stages, "list")
			}
		} else if strings.HasSuffix(op.path, "/retry") || strings.HasSuffix(op.path, "/bounce") {
			stages = append(stages, "read")
		}
		for _, stage := range stages {
			t.Run(op.method+op.path+"/"+stage, func(t *testing.T) {
				c := &queueRequestDB{stage: stage}
				rr := serveQueueRequest(t, c, op.method, op.path, op.body, time.Second)
				if rr.Code != 500 || strings.Contains(rr.Body.String(), "private") {
					t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
				}
				if stage != "commit" && c.commits.Load() != 0 {
					t.Fatalf("failed operation committed: %d", c.commits.Load())
				}
			})
		}
	}
}

func TestQueueSingleMutationLocksAndPreservesRefusals(t *testing.T) {
	for _, action := range []string{"retry", "bounce"} {
		for _, scenario := range []string{"success", "missing", "zero", "delivered", "write"} {
			t.Run(action+"/"+scenario, func(t *testing.T) {
				c := &queueRequestDB{missing: scenario == "missing", zeroAffected: scenario == "zero"}
				want := 200
				if scenario == "missing" || scenario == "zero" {
					want = 404
				}
				if scenario == "write" {
					c.stage, want = "write", 500
				}
				if scenario == "delivered" {
					c.status = "delivered"
					if action == "retry" {
						want = 400
					}
				}
				rr := serveQueueRequest(t, c, "POST", "/api/v1/admin/queue/1/"+action, "", time.Second)
				if rr.Code != want || c.beginCalls != 1 || c.lockReads != 1 {
					t.Fatalf("status=%d want=%d begins=%d lockedReads=%d body=%s", rr.Code, want, c.beginCalls, c.lockReads, rr.Body)
				}
				if want == 200 {
					if c.writes != 1 || c.commits.Load() != 1 || !strings.Contains(rr.Body.String(), `"status":"`+map[string]string{"retry": "retrying", "bounce": "bounced"}[action]+`"`) {
						t.Fatalf("success writes=%d commits=%d body=%s", c.writes, c.commits.Load(), rr.Body)
					}
				} else if c.commits.Load() != 0 || c.rollbacks.Load() != 1 || ((want == 400 || scenario == "missing") && c.writes != 0) {
					t.Fatalf("refusal writes=%d commits=%d rollbacks=%d", c.writes, c.commits.Load(), c.rollbacks.Load())
				}
			})
		}
	}
}

func TestQueueMutationCancellationAfterLockedRead(t *testing.T) {
	for _, action := range []string{"retry", "bounce"} {
		t.Run(action, func(t *testing.T) {
			c := &queueRequestDB{stage: "wait-write"}
			rr := serveQueueRequest(t, c, "POST", "/api/v1/admin/queue/1/"+action, "", 20*time.Millisecond)
			if rr.Code != 500 || c.contextMissing || c.lockReads != 1 || c.writes != 1 || c.commits.Load() != 0 {
				t.Fatalf("status=%d contextMissing=%t locks=%d writes=%d commits=%d", rr.Code, c.contextMissing, c.lockReads, c.writes, c.commits.Load())
			}
		})
	}
}
