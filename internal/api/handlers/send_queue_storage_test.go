package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/restmail/restmail/internal/auth"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Sending a message keeps a storage failure a failure (#1163). A recipient
// lookup that failed was taken for a remote recipient, queueing a local
// recipient's copy for the internet, and a queue insert that failed was
// ignored, answering 201 sent with nothing queued to send it.

const sendRemoteRecipient = "far@remote.example"

type sendQueueDB struct {
	serviceProbeTestDB
	failLookup, failQueue bool

	mu      sync.Mutex
	queued  int
	commits int
}

func (d *sendQueueDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *sendQueueDB) Driver() driver.Driver                        { return d }
func (d *sendQueueDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *sendQueueDB) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	recipient := false
	for _, a := range args {
		if s, ok := a.Value.(string); ok && strings.EqualFold(s, sendRemoteRecipient) {
			recipient = true
		}
	}
	if rows, ok := defaultGrantRows(q); ok {
		return rows, nil
	}
	switch {
	case strings.HasPrefix(q, `INSERT INTO "outbound_queue"`):
		if d.failQueue {
			return nil, errors.New("private queue store failure")
		}
		d.queued++
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(70)}}, nil
	case strings.HasPrefix(q, "INSERT"):
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(60)}}, nil
	case strings.Contains(q, `FROM "mailboxes"`) && recipient:
		if d.failLookup {
			return nil, errors.New("private recipient lookup failure")
		}
		return &deliveryReadRows{}, nil
	case strings.Contains(q, `FROM "domains"`):
		return &deliveryReadRows{columns: []string{"id", "name", "active", "sending_policy"},
			values: []driver.Value{int64(3), "example.test", true, ""}}, nil
	case strings.Contains(q, `FROM "mailboxes"`):
		return &deliveryReadRows{columns: []string{"id", "address", "active", "domain_id", "display_name"},
			values: []driver.Value{int64(7), "owner@example.test", true, int64(3), "Owner"}}, nil
	}
	return &deliveryReadRows{}, nil
}

func (d *sendQueueDB) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

func (d *sendQueueDB) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &sendQueueTx{d}, nil
}

type sendQueueTx struct{ d *sendQueueDB }

func (tx *sendQueueTx) Commit() error {
	tx.d.mu.Lock()
	tx.d.commits++
	tx.d.mu.Unlock()
	return nil
}
func (tx *sendQueueTx) Rollback() error { return nil }

func sendWith(t *testing.T, d *sendQueueDB) (int, string) {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	h := NewMessageHandler(db, nil, nil, "", nil)
	rr := sendAs(h, &auth.Claims{AccountID: 7, Address: "owner@example.test", CredentialID: 3, CredentialKind: "app_password"},
		`{"from":"owner@example.test","to":["`+sendRemoteRecipient+`"],"subject":"hello","body_text":"x"}`)
	return rr.Code, rr.Body.String()
}

func TestSendKeepsRecipientAndQueueFailures(t *testing.T) {
	ok := &sendQueueDB{}
	if code, body := sendWith(t, ok); code != http.StatusCreated || ok.queued != 1 {
		t.Fatalf("a remote send: %d %s, %d queued", code, body, ok.queued)
	}

	lookup := &sendQueueDB{failLookup: true}
	if code, body := sendWith(t, lookup); code != http.StatusInternalServerError || lookup.queued != 0 {
		t.Errorf("a failed recipient lookup: %d %s, %d queued; want 500 and nothing queued", code, body, lookup.queued)
	}

	queue := &sendQueueDB{failQueue: true}
	code, body := sendWith(t, queue)
	if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"deferred"`) {
		t.Errorf("a failed queue insert: %d %s; want 503 deferred", code, body)
	}
	if strings.Contains(body, "private") {
		t.Errorf("the dependency failure reached the client: %s", body)
	}
}
