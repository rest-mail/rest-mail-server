package auditanchor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This transactional driver exercises the production GORM path without a
// service. It rejects unexpected SQL and retains writes only at commit.
type anchorDB struct {
	previous                models.AuditEntry
	head                    audit.Anchor
	changes                 changeWindow
	owners                  []string
	locked                  bool
	fail                    string
	queue, pendingQueue     []map[string]driver.Value
	entries, pendingEntries []map[string]driver.Value
	committed, rolledBack   int
	adminAfter              int64
	ownerSQL                string
}

func (d *anchorDB) Connect(context.Context) (driver.Conn, error)                 { return d, nil }
func (d *anchorDB) Driver() driver.Driver                                        { return d }
func (d *anchorDB) Open(string) (driver.Conn, error)                             { return d, nil }
func (d *anchorDB) Prepare(string) (driver.Stmt, error)                          { return nil, errors.New("unexpected prepare") }
func (d *anchorDB) Close() error                                                 { return nil }
func (d *anchorDB) Begin() (driver.Tx, error)                                    { return &anchorTx{d}, nil }
func (d *anchorDB) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return d.Begin() }
func (d *anchorDB) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, "SELECT pg_try_advisory_xact_lock") {
		return rows([]string{"locked"}, []driver.Value{d.locked}), nil
	}
	if strings.Contains(q, `FROM "audit_log"`) && strings.Contains(q, `"action"`) {
		return nil, fmt.Errorf("unexpected quoted action query %s", q)
	}
	if strings.Contains(q, `FROM "audit_log"`) && strings.Contains(q, "action =") {
		cols := []string{"seq", "occurred_at", "action", "outcome", "target"}
		if d.previous.Seq == 0 {
			return rows(cols), nil
		}
		target, _ := json.Marshal(d.previous.Target)
		return rows(cols, []driver.Value{d.previous.Seq, d.previous.OccurredAt, emailAction, models.AuditOutcomeSuccess, string(target)}), nil
	}
	if strings.Contains(q, "MIN(occurred_at)") {
		if len(args) < 1 || args[0].Value != d.adminAfter {
			return nil, fmt.Errorf("admin changes start at %v; want last anchored seq %d (entries between the snapshot and publication must not be lost)", args, d.adminAfter)
		}
		if !strings.Contains(q, "method NOT IN ('GET', 'HEAD', 'OPTIONS')") || !strings.Contains(q, "outcome =") || !strings.Contains(q, "owner_keys.%") || !strings.Contains(q, "grants.%") {
			return nil, fmt.Errorf("unsafe admin change selection: %s", q)
		}
		if d.changes.First.IsZero() {
			return rows([]string{"first", "last"}), nil
		}
		return rows([]string{"first", "last"}, []driver.Value{d.changes.First, d.changes.Last}), nil
	}
	if strings.Contains(q, `FROM "audit_log"`) || strings.Contains(q, "SELECT seq, hash FROM audit_log") {
		return rows([]string{"seq", "hash"}, []driver.Value{d.head.Seq, d.head.Hash}), nil
	}
	if strings.Contains(q, `FROM "mailboxes"`) {
		d.ownerSQL = q
		var values [][]driver.Value
		for _, address := range d.owners {
			values = append(values, []driver.Value{address})
		}
		return rows([]string{"address"}, values...), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "outbound_queue"`) {
		if d.fail == "queue" {
			return nil, errors.New("injected queue failure")
		}
		d.pendingQueue = append(d.pendingQueue, insertValues(q, args))
		return rows([]string{"id"}, []driver.Value{int64(len(d.pendingQueue))}), nil
	}
	return nil, fmt.Errorf("unexpected query: %s", q)
}
func (d *anchorDB) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "SAVEPOINT") || strings.HasPrefix(q, "ROLLBACK TO SAVEPOINT") || strings.HasPrefix(q, "SELECT pg_advisory_xact_lock") {
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "audit_log"`) {
		if d.fail == "audit" {
			return nil, errors.New("injected audit failure")
		}
		d.pendingEntries = append(d.pendingEntries, insertValues(q, args))
		return driver.RowsAffected(1), nil
	}
	return nil, fmt.Errorf("unexpected exec: %s", q)
}
func insertValues(q string, args []driver.NamedValue) map[string]driver.Value {
	start := strings.IndexByte(q, '(') + 1
	end := strings.IndexByte(q, ')')
	out := map[string]driver.Value{}
	for i, col := range strings.Split(q[start:end], ",") {
		out[strings.Trim(col, `"`)] = args[i].Value
	}
	return out
}

type anchorTx struct{ d *anchorDB }

func (tx *anchorTx) Commit() error {
	if tx.d.fail == "commit" {
		return errors.New("injected commit failure")
	}
	tx.d.committed++
	tx.d.queue = append(tx.d.queue, tx.d.pendingQueue...)
	tx.d.entries = append(tx.d.entries, tx.d.pendingEntries...)
	tx.d.pendingQueue = nil
	tx.d.pendingEntries = nil
	return nil
}
func (tx *anchorTx) Rollback() error {
	tx.d.rolledBack++
	tx.d.pendingQueue = nil
	tx.d.pendingEntries = nil
	return nil
}

type anchorRows struct {
	cols   []string
	values [][]driver.Value
}

func rows(cols []string, values ...[]driver.Value) *anchorRows {
	return &anchorRows{cols: cols, values: values}
}
func (r *anchorRows) Columns() []string { return r.cols }
func (r *anchorRows) Close() error      { return nil }
func (r *anchorRows) Next(out []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(out, r.values[0])
	r.values = r.values[1:]
	return nil
}
func workerDB(t *testing.T, d *anchorDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(d)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func workerFixture() *anchorDB {
	return &anchorDB{locked: true, head: audit.Anchor{Seq: 42, Hash: strings.Repeat("b", 64)}, owners: []string{"owner@example.test"}, adminAfter: 0}
}

func TestWorkerQueuesAnchorAndChainedAuditAtomically(t *testing.T) {
	d := workerFixture()
	w := NewWorker(workerDB(t, d), "mx.example.test", nil)
	sent, err := w.Check(context.Background(), testNow)
	if err != nil || !sent {
		t.Fatalf("Check: %v, %v", sent, err)
	}
	if len(d.queue) != 1 || len(d.entries) != 1 || d.committed != 1 {
		t.Fatalf("committed queue=%d entries=%d transactions=%d", len(d.queue), len(d.entries), d.committed)
	}
	q, e := d.queue[0], d.entries[0]
	if q["recipient"] != "owner@example.test" || q["sender"] != "audit-anchor@example.test" {
		t.Fatalf("owner delivery: %v", q)
	}
	if e["seq"] != int64(43) || e["prev_hash"] != d.head.Hash || e["action"] != emailAction || e["hash"] == "" {
		t.Fatalf("unchained audit: %v", e)
	}
	var target models.AuditTarget
	if err := json.Unmarshal([]byte(e["target"].(string)), &target); err != nil {
		t.Fatal(err)
	}
	if target["seq"] != "42" || target["hash"] != d.head.Hash || target["delivery"] != "queued" {
		t.Fatalf("audit target: %v", target)
	}
	for _, part := range []string{"mailboxes.active", "roles.name", "grants.scope", "grants.mode IN", "grants.expires_at"} {
		if !strings.Contains(d.ownerSQL, part) {
			t.Errorf("owner query missing %q: %s", part, d.ownerSQL)
		}
	}
}

func TestWorkerFailureRollsBackAndCanRetry(t *testing.T) {
	for _, failure := range []string{"queue", "audit", "commit"} {
		t.Run(failure, func(t *testing.T) {
			d := workerFixture()
			d.fail = failure
			w := NewWorker(workerDB(t, d), "mx.example.test", []string{"external@outside.test"})
			sent, err := w.Check(context.Background(), testNow)
			if err == nil || sent || len(d.queue) != 0 || len(d.entries) != 0 {
				t.Fatalf("failed publication committed: %v %v queue=%d entries=%d", sent, err, len(d.queue), len(d.entries))
			}
			// A commit error may leave the connection's state unknown; a fresh fixture
			// models a clean retry against a database where the commit did not happen.
			if failure == "commit" {
				return
			}
			d.fail = ""
			sent, err = w.Check(context.Background(), testNow)
			if err != nil || !sent || len(d.queue) != 1 || len(d.entries) != 1 {
				t.Fatalf("retry: %v %v queue=%d entries=%d", sent, err, len(d.queue), len(d.entries))
			}
		})
	}
}

func TestWorkerRestartsReplicasAndConcurrentAdminChanges(t *testing.T) {
	t.Run("replica owns publication lock", func(t *testing.T) {
		d := workerFixture()
		d.locked = false
		sent, err := NewWorker(workerDB(t, d), "mx.test", nil).Check(context.Background(), testNow)
		if err != nil || sent || len(d.queue) != 0 {
			t.Fatalf("duplicate replica publication: %v %v", sent, err)
		}
	})
	t.Run("restart respects durable hourly limit", func(t *testing.T) {
		d := workerFixture()
		d.previous = previousAnchor(testNow.Add(-time.Minute))
		sent, err := NewWorker(workerDB(t, d), "mx.test", nil).Check(context.Background(), testNow)
		if err != nil || sent || len(d.queue) != 0 {
			t.Fatalf("restart ignored limit: %v %v", sent, err)
		}
	})
	t.Run("admin append between snapshot and publication", func(t *testing.T) {
		d := workerFixture()
		d.previous = previousAnchor(testNow.Add(-2 * time.Hour))
		d.adminAfter = 10
		d.changes = changeWindow{First: testNow.Add(-10 * time.Minute), Last: testNow.Add(-5 * time.Minute)}
		sent, err := NewWorker(workerDB(t, d), "mx.test", nil).Check(context.Background(), testNow)
		if err != nil || !sent {
			t.Fatalf("unanchored concurrent change lost: %v %v", sent, err)
		}
	})
	t.Run("missing owners leaves no publication", func(t *testing.T) {
		d := workerFixture()
		d.owners = nil
		sent, err := NewWorker(workerDB(t, d), "mx.test", nil).Check(context.Background(), testNow)
		if err == nil || sent || len(d.entries) != 0 || len(d.queue) != 0 {
			t.Fatalf("missing owners recorded a sent anchor: %v %v", sent, err)
		}
	})
}
