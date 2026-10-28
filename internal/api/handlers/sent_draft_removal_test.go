package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sentDraftDB answers the DELETE that removes a sent draft, failing the first
// failures of them, and records whether each ran under a cancelled context.
type sentDraftDB struct {
	serviceProbeTestDB
	mu        sync.Mutex
	failures  int
	deletes   int
	cancelled int
	commits   int
	rollbacks int
}

func (d *sentDraftDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *sentDraftDB) Driver() driver.Driver                        { return d }
func (d *sentDraftDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *sentDraftDB) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &sentDraftTx{d}, nil
}

type sentDraftTx struct{ d *sentDraftDB }

func (tx *sentDraftTx) Commit() error {
	tx.d.mu.Lock()
	defer tx.d.mu.Unlock()
	tx.d.commits++
	return nil
}
func (tx *sentDraftTx) Rollback() error {
	tx.d.mu.Lock()
	defer tx.d.mu.Unlock()
	tx.d.rollbacks++
	return nil
}

func (d *sentDraftDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !strings.Contains(q, `DELETE FROM "messages"`) {
		return nil, errors.New("unexpected SQL: " + q)
	}
	d.deletes++
	if ctx.Err() != nil {
		d.cancelled++
		return nil, ctx.Err()
	}
	if d.deletes <= d.failures {
		return nil, errors.New("private storage failure")
	}
	return driver.RowsAffected(1), nil
}

func sentDraftGorm(t *testing.T, d *sentDraftDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(d)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{
		DisableAutomaticPing: true, SkipDefaultTransaction: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// A sent draft is removed even when the request that sent it has gone, and a
// failed removal is tried again and reported, never dropped in silence
// (#1265). The message is already sent by then, so a draft left in Drafts
// invites a second send.
func TestASentDraftIsRemovedAndAFailureIsNotSilent(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	draft := models.Message{ID: 5, MailboxID: 1, IsDraft: true}

	ok := &sentDraftDB{}
	if err := removeSentDraft(cancelled, sentDraftGorm(t, ok), &draft); err != nil {
		t.Fatalf("removal under a gone request: %v", err)
	}
	if ok.deletes != 1 || ok.cancelled != 0 {
		t.Fatalf("deletes %d, under a cancelled context %d", ok.deletes, ok.cancelled)
	}

	once := &sentDraftDB{failures: 1}
	if err := removeSentDraft(context.Background(), sentDraftGorm(t, once), &draft); err != nil {
		t.Fatalf("one failure then success: %v", err)
	}
	if once.deletes != 2 {
		t.Fatalf("a failed removal was not tried again: %d deletes", once.deletes)
	}
	if once.commits != 1 || once.rollbacks != 1 {
		t.Fatalf("retry transactions: %d commits, %d rollbacks", once.commits, once.rollbacks)
	}

	always := &sentDraftDB{failures: 10}
	if err := removeSentDraft(context.Background(), sentDraftGorm(t, always), &draft); err == nil {
		t.Fatal("a removal that kept failing reported success")
	}
	if always.commits != 0 || always.rollbacks != sentDraftRemovalAttempts {
		t.Fatalf("failed removal transactions: %d commits, %d rollbacks", always.commits, always.rollbacks)
	}
}
