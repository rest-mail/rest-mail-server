package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/ratelimit"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A scheduled send whose retry cannot be recorded is not left as sending. The
// dispatcher only picks up pending rows, so a row left as sending is never
// sent, never failed and cannot be cancelled: the send was silently lost.

// scheduledRetryDB fails the update that puts a send back to pending, and
// records the statuses every other update writes.
type scheduledRetryDB struct {
	serviceProbeTestDB

	mu       sync.Mutex
	statuses []string
}

func (d *scheduledRetryDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *scheduledRetryDB) Driver() driver.Driver                        { return d }
func (d *scheduledRetryDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *scheduledRetryDB) ExecContext(_ context.Context, _ string, args []driver.NamedValue) (driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range args {
		switch a.Value {
		case "pending":
			return nil, errors.New("private scheduled send store failure")
		case "sent", "failed":
			d.statuses = append(d.statuses, a.Value.(string))
		}
	}
	return driver.RowsAffected(1), nil
}

func (d *scheduledRetryDB) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &deliveryReadRows{}, nil
}

func (d *scheduledRetryDB) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return scheduledRetryTx{}, nil
}

type scheduledRetryTx struct{}

func (scheduledRetryTx) Commit() error   { return nil }
func (scheduledRetryTx) Rollback() error { return nil }

func TestScheduledSendIsNotLeftSendingWhenItsRetryCannotBeRecorded(t *testing.T) {
	d := &scheduledRetryDB{}
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}

	// The account's send rate is spent, so the send is answered 429 and is
	// to be retried.
	limiter := ratelimit.NewSubmissionLimiter(1, 100)
	limiter.Allow("7")
	h := NewMessageHandler(db, nil, nil, "", nil)
	h.SetSendLimits(0, limiter)

	request, _ := json.Marshal(map[string]any{"from": "owner@example.test", "to": []string{"far@remote.example"},
		"subject": "later", "body_text": "x"})
	h.deliverScheduled(context.Background(), models.ScheduledSend{ID: 5, AccountID: 7, Status: "sending",
		Request: models.JSONB(request)})

	if len(d.statuses) != 1 || d.statuses[0] != "failed" {
		t.Fatalf("after a retry that could not be recorded the send was set to %q; want it failed, not left sending", d.statuses)
	}
}
