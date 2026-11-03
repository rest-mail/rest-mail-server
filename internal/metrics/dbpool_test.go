package metrics

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// histogramCount is the number of observations a histogram series holds.
func histogramCount(t *testing.T, h prometheus.Observer) uint64 {
	t.Helper()
	var m dto.Metric
	if err := h.(prometheus.Metric).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}

// TestObserveIMAPCommand_BoundedLabel: a known command is observed under its
// own name, whatever its case, and any other name under "other", so a client
// cannot add series.
func TestObserveIMAPCommand_BoundedLabel(t *testing.T) {
	fetch := histogramCount(t, IMAPCommandDuration.WithLabelValues("FETCH"))
	other := histogramCount(t, IMAPCommandDuration.WithLabelValues("other"))
	before := testutil.CollectAndCount(IMAPCommandDuration)

	ObserveIMAPCommand("fetch", 20*time.Millisecond)
	ObserveIMAPCommand("XYZZY", time.Millisecond)
	ObserveIMAPCommand("x-made-up-1", time.Millisecond)

	if got := histogramCount(t, IMAPCommandDuration.WithLabelValues("FETCH")); got != fetch+1 {
		t.Errorf("FETCH observations = %d, want %d", got, fetch+1)
	}
	if got := histogramCount(t, IMAPCommandDuration.WithLabelValues("other")); got != other+2 {
		t.Errorf("other observations = %d, want %d", got, other+2)
	}
	if got := testutil.CollectAndCount(IMAPCommandDuration); got != before {
		t.Errorf("unknown commands added series: %d, was %d", got, before)
	}
}

// fakeConnector opens connections that never talk to anything, so a pool's
// accounting can be exercised without a database.
type fakeConnector struct{}

type fakeConn struct{}

func (fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn{}, nil }
func (fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return fakeConn{}, nil }

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("fake") }

// TestDBPool_ReportsTrackedPool: the restmail_db_pool_* series follow the
// tracked pool: its limit, the connections in use and idle, and a caller
// that had to wait for a connection.
func TestDBPool_ReportsTrackedPool(t *testing.T) {
	prev := dbPool.db.Load()
	t.Cleanup(func() { dbPool.db.Store(prev) })

	db := sql.OpenDB(fakeConnector{})
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	TrackDBPool(db)

	ctx := context.Background()
	first, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *sql.Conn)
	go func() {
		c, _ := db.Conn(ctx)
		got <- c
	}()
	// The second caller queues behind the only connection.
	deadline := time.Now().Add(5 * time.Second)
	for db.Stats().WaitCount == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the second caller never waited")
		}
		time.Sleep(time.Millisecond)
	}
	expect := `
# HELP restmail_db_pool_in_use_connections Database pool connections in use
# TYPE restmail_db_pool_in_use_connections gauge
restmail_db_pool_in_use_connections 1
# HELP restmail_db_pool_max_open_connections Maximum open connections the database pool allows (0: unlimited)
# TYPE restmail_db_pool_max_open_connections gauge
restmail_db_pool_max_open_connections 1
# HELP restmail_db_pool_wait_count_total Times a caller waited for a database pool connection
# TYPE restmail_db_pool_wait_count_total counter
restmail_db_pool_wait_count_total 1
`
	if err := testutil.CollectAndCompare(dbPool, strings.NewReader(expect),
		"restmail_db_pool_in_use_connections", "restmail_db_pool_max_open_connections", "restmail_db_pool_wait_count_total"); err != nil {
		t.Error(err)
	}

	first.Close()
	second := <-got
	second.Close()
	expect = `
# HELP restmail_db_pool_idle_connections Idle database pool connections
# TYPE restmail_db_pool_idle_connections gauge
restmail_db_pool_idle_connections 1
# HELP restmail_db_pool_in_use_connections Database pool connections in use
# TYPE restmail_db_pool_in_use_connections gauge
restmail_db_pool_in_use_connections 0
# HELP restmail_db_pool_open_connections Open connections in the database pool, in use and idle
# TYPE restmail_db_pool_open_connections gauge
restmail_db_pool_open_connections 1
`
	if err := testutil.CollectAndCompare(dbPool, strings.NewReader(expect),
		"restmail_db_pool_idle_connections", "restmail_db_pool_in_use_connections", "restmail_db_pool_open_connections"); err != nil {
		t.Error(err)
	}
	var wait dto.Metric
	ch := make(chan prometheus.Metric, 6)
	dbPool.Collect(ch)
	close(ch)
	for m := range ch {
		if strings.Contains(m.Desc().String(), "restmail_db_pool_wait_duration_seconds_total") {
			if err := m.Write(&wait); err != nil {
				t.Fatal(err)
			}
		}
	}
	if wait.GetCounter().GetValue() <= 0 {
		t.Errorf("restmail_db_pool_wait_duration_seconds_total = %v after a wait, want > 0", wait.GetCounter().GetValue())
	}
}

// TestDBPool_NothingWithoutAPool: a process that never opened the database
// exports no pool series.
func TestDBPool_NothingWithoutAPool(t *testing.T) {
	prev := dbPool.db.Load()
	t.Cleanup(func() { dbPool.db.Store(prev) })
	dbPool.db.Store(nil)
	if n := testutil.CollectAndCount(dbPool); n != 0 {
		t.Errorf("%d pool series without a pool, want 0", n)
	}
}

// TestIsDBConnectionError: an unreachable database counts; an answer from it,
// or a caller cancelling, does not.
func TestIsDBConnectionError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"query error", &pgconn.PgError{Code: "23505"}, false},
		{"not found", gorm.ErrRecordNotFound, false},
		{"cancelled", context.Canceled, false},
		{"bad conn", driver.ErrBadConn, true},
		{"network", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"wrapped network", errors.Join(errors.New("query"), &net.OpError{Op: "read", Err: errors.New("reset")}), true},
	} {
		if got := IsDBConnectionError(tc.err); got != tc.want {
			t.Errorf("%s: IsDBConnectionError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestInstrumentDB_CountsUnreachableDatabase: with InstrumentDB on a
// connection, an operation against a database nobody listens for adds one to
// restmail_db_connection_errors_total.
func TestInstrumentDB_CountsUnreachableDatabase(t *testing.T) {
	prev := dbPool.db.Load()
	t.Cleanup(func() { dbPool.db.Store(prev) })

	// A port nothing listens on: reserve one and close it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	l.Close()
	dsn := fmt.Sprintf("host=127.0.0.1 port=%d user=x password=x dbname=x sslmode=disable connect_timeout=2", addr.Port)
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := InstrumentDB(gdb); err != nil {
		t.Fatal(err)
	}
	before := testutil.ToFloat64(DBConnectionErrors)
	if err := gdb.Exec("SELECT 1").Error; err == nil {
		t.Fatal("SELECT 1 against a closed port succeeded")
	}
	var n int64
	_ = gdb.Raw("SELECT 1").Scan(&n).Error
	if got := testutil.ToFloat64(DBConnectionErrors); got != before+2 {
		t.Errorf("restmail_db_connection_errors_total = %v, want %v after two operations on an unreachable database", got, before+2)
	}
	if InstrumentDB(nil) != nil {
		t.Error("InstrumentDB(nil) failed")
	}
}
