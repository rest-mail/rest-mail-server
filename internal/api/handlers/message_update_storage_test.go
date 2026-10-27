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
	"github.com/restmail/restmail/internal/auth"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Updating a message and replying to a calendar invite keep a storage failure
// a failure (#1163). updateMessage ignored a failed update and a failed
// read-back, answering 200 — and announcing the change on the event stream —
// for a change that was not made. respondToCalendar took a failed lookup of
// the organizer for a remote organizer, queueing a local organizer's reply
// for the internet, and ignored a failed queue insert, answering "sent" with
// nothing queued to send it.

type messageStorageKey struct{}

const organizerAddress = "organizer@remote.example"

type messageStorageDB struct {
	serviceProbeTestDB
	failUpdate, failReload  bool
	failOrganizer, notLocal bool
	failQueue               bool

	mu                 sync.Mutex
	messageReads       int
	seen, lost, writes []string
	commits, rollbacks int
}

func (d *messageStorageDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *messageStorageDB) Driver() driver.Driver                        { return d }
func (d *messageStorageDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *messageStorageDB) observe(ctx context.Context, q string) {
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(messageStorageKey{}) != true {
		d.lost = append(d.lost, q)
	}
}

var errMessageStore = errors.New("message store lost at 10.0.0.9")

func (d *messageStorageDB) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observe(ctx, q)
	organizer := false
	for _, a := range args {
		if s, ok := a.Value.(string); ok && strings.EqualFold(s, organizerAddress) {
			organizer = true
		}
	}
	switch {
	case strings.HasPrefix(q, "INSERT"):
		d.writes = append(d.writes, q)
		if d.failQueue && strings.Contains(q, `"outbound_queue"`) {
			return nil, errMessageStore
		}
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(50)}}, nil
	case strings.Contains(q, `FROM "messages"`):
		d.messageReads++
		if d.failReload && d.messageReads == 2 {
			return nil, errMessageStore
		}
		return &deliveryReadRows{
			columns: []string{"id", "mailbox_id", "folder", "sender", "subject", "calendar_events", "received_at"},
			values: []driver.Value{int64(9), int64(7), "INBOX", organizerAddress, "Meeting",
				`[{"method":"REQUEST","uid":"u-1","summary":"Meeting","organizer":{"address":"` + organizerAddress + `"}}]`,
				time.Now()}}, nil
	case strings.Contains(q, `FROM "mailboxes"`) && organizer:
		if d.failOrganizer {
			return nil, errMessageStore
		}
		return &deliveryReadRows{}, nil
	case strings.Contains(q, `FROM "mailboxes"`):
		return &deliveryReadRows{columns: []string{"id", "address", "active", "domain_id", "display_name"},
			values: []driver.Value{int64(7), "owner@example.test", true, int64(3), "Owner"}}, nil
	}
	return &deliveryReadRows{}, nil
}

func (d *messageStorageDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observe(ctx, q)
	d.writes = append(d.writes, q)
	if d.failUpdate && strings.HasPrefix(q, `UPDATE "messages"`) {
		return nil, errMessageStore
	}
	return driver.RowsAffected(1), nil
}

func (d *messageStorageDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.observe(ctx, "BEGIN")
	return &messageStorageTx{d}, nil
}

type messageStorageTx struct{ d *messageStorageDB }

func (tx *messageStorageTx) Commit() error {
	tx.d.mu.Lock()
	tx.d.commits++
	tx.d.mu.Unlock()
	return nil
}
func (tx *messageStorageTx) Rollback() error {
	tx.d.mu.Lock()
	tx.d.rollbacks++
	tx.d.mu.Unlock()
	return nil
}

func serveMessage(t *testing.T, d *messageStorageDB, method, path, body string) *httptest.ResponseRecorder {
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
	h := NewMessageHandler(db, nil, nil, "", nil)
	h.RegisterMessages(rs)
	h.RegisterCompose(rs)

	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), messageStorageKey{}, true), 4*time.Second)
	t.Cleanup(cancel)
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r = withClaims(r, &auth.Claims{AccountID: 7, Address: "owner@example.test", CredentialID: 3, CredentialKind: "app_password"})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, r)
	return rr
}

func TestUpdateMessageKeepsAStorageFailure(t *testing.T) {
	ok := &messageStorageDB{}
	if rr := serveMessage(t, ok, http.MethodPatch, "/api/v1/messages/9", `{"is_read":true}`); rr.Code != http.StatusOK {
		t.Fatalf("update: %d %s, want 200", rr.Code, rr.Body.String())
	}
	if len(ok.lost) > 0 {
		t.Errorf("%d of %d statements ran without the request's context, first %q", len(ok.lost), len(ok.seen), ok.lost[0])
	}

	for name, d := range map[string]*messageStorageDB{
		"failed update":    {failUpdate: true},
		"failed read-back": {failReload: true},
	} {
		rr := serveMessage(t, d, http.MethodPatch, "/api/v1/messages/9", `{"is_read":true}`)
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("%s: %d %s, want 500", name, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "10.0.0.9") {
			t.Errorf("%s: the dependency failure reached the client: %s", name, rr.Body.String())
		}
		if d.commits != 0 {
			t.Errorf("%s: %d commits, want the change rolled back", name, d.commits)
		}
	}
}

func TestCalendarReplyKeepsAStorageFailure(t *testing.T) {
	reply := `{"response":"ACCEPTED","from":"owner@example.test"}`

	// A remote organizer: the Sent copy and its queue entry commit together.
	remote := &messageStorageDB{}
	if rr := serveMessage(t, remote, http.MethodPost, "/api/v1/messages/9/calendar-reply", reply); rr.Code != http.StatusOK {
		t.Fatalf("remote reply: %d %s, want 200", rr.Code, rr.Body.String())
	}
	if remote.commits != 1 || len(remote.lost) > 0 {
		t.Errorf("remote reply: %d commits, %d statements without the request's context", remote.commits, len(remote.lost))
	}

	// A failed organizer lookup sends nothing anywhere.
	lookup := &messageStorageDB{failOrganizer: true}
	rr := serveMessage(t, lookup, http.MethodPost, "/api/v1/messages/9/calendar-reply", reply)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("failed organizer lookup: %d %s, want 500", rr.Code, rr.Body.String())
	}
	if len(lookup.writes) > 0 {
		t.Errorf("failed organizer lookup wrote %q", lookup.writes)
	}

	// A queue insert that fails leaves no Sent copy claiming the reply went.
	queue := &messageStorageDB{failQueue: true}
	rr = serveMessage(t, queue, http.MethodPost, "/api/v1/messages/9/calendar-reply", reply)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("failed queue insert: %d %s, want 500", rr.Code, rr.Body.String())
	}
	if queue.commits != 0 {
		t.Errorf("failed queue insert: %d commits, want the Sent copy rolled back", queue.commits)
	}
}
