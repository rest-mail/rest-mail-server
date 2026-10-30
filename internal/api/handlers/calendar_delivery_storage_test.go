package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A delivered invite keeps its calendar data: the events are stored with the
// message and the version rows in its transaction, so a failure fails the
// delivery for the sender to retry rather than leaving an invite nobody can
// answer (#1266).

const inviteRaw = "From: organizer@remote.example\r\n" +
	"To: one@example.test\r\n" +
	"Subject: Planning\r\n" +
	"Message-ID: <invite-1@remote.example>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=b1\r\n" +
	"\r\n" +
	"--b1\r\n" +
	"Content-Type: text/plain\r\n" +
	"\r\n" +
	"Join us.\r\n" +
	"--b1\r\n" +
	"Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n" +
	"\r\n" +
	"BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"METHOD:REQUEST\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:planning-1@remote.example\r\n" +
	"SEQUENCE:0\r\n" +
	"DTSTART:20261101T100000Z\r\n" +
	"DTEND:20261101T110000Z\r\n" +
	"SUMMARY:Planning\r\n" +
	"ORGANIZER:mailto:organizer@remote.example\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n" +
	"--b1--\r\n"

type calendarDeliveryDB struct {
	serviceProbeTestDB
	failVersions bool

	mu                sync.Mutex
	messageInserts    []string
	versions, commits int
	lateCalendar      int
}

func (d *calendarDeliveryDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *calendarDeliveryDB) Driver() driver.Driver                        { return d }
func (d *calendarDeliveryDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *calendarDeliveryDB) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if rows, ok := receiveFixtureRows(q); ok {
		return rows, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case strings.HasPrefix(q, `INSERT INTO "calendar_event_versions"`):
		if d.failVersions {
			return nil, errors.New("private calendar store failure")
		}
		d.versions++
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(81)}}, nil
	case strings.HasPrefix(q, `INSERT INTO "messages"`):
		var b strings.Builder
		b.WriteString(q)
		for _, a := range args {
			if v, ok := a.Value.([]byte); ok {
				b.Write(v)
			}
			if v, ok := a.Value.(string); ok {
				b.WriteString(v)
			}
		}
		d.messageInserts = append(d.messageInserts, b.String())
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(80)}}, nil
	case strings.HasPrefix(q, "INSERT"):
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(82)}}, nil
	case strings.Contains(q, `FROM "mailboxes"`):
		return &deliveryReadRows{columns: []string{"id", "domain_id", "address", "active", "quota_bytes"},
			values: []driver.Value{int64(2), int64(1), "one@example.test", true, int64(1 << 30)}}, nil
	}
	return &deliveryReadRows{}, nil
}

func (d *calendarDeliveryDB) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if strings.Contains(q, `"calendar_events"`) {
		d.lateCalendar++
	}
	return driver.RowsAffected(1), nil
}

func (d *calendarDeliveryDB) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &calendarDeliveryTx{d}, nil
}

type calendarDeliveryTx struct{ d *calendarDeliveryDB }

func (tx *calendarDeliveryTx) Commit() error {
	tx.d.mu.Lock()
	tx.d.commits++
	tx.d.mu.Unlock()
	return nil
}
func (tx *calendarDeliveryTx) Rollback() error { return nil }

func deliverInvite(t *testing.T, d *calendarDeliveryDB) *httptest.ResponseRecorder {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	NewMessageHandler(db, nil, nil, "", nil).RegisterDelivery(
		endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()},
		endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }})
	body, _ := json.Marshal(map[string]any{"mailbox_id": 2, "sender": "organizer@remote.example",
		"subject": "Planning", "body_text": "Join us.", "raw_message": []byte(inviteRaw)})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, deliveryRequest(t, string(body), "/api/v1/messages/deliver"))
	return rr
}

func TestDeliveredInviteKeepsItsCalendarData(t *testing.T) {
	stored := &calendarDeliveryDB{}
	if rr := deliverInvite(t, stored); rr.Code != http.StatusCreated {
		t.Fatalf("invite: %d %s, want 201", rr.Code, rr.Body.String())
	}
	if len(stored.messageInserts) != 1 || !strings.Contains(stored.messageInserts[0], "calendar_events") ||
		!strings.Contains(stored.messageInserts[0], "planning-1@remote.example") {
		t.Errorf("the message was stored without its calendar events: %q", stored.messageInserts)
	}
	if stored.lateCalendar != 0 {
		t.Errorf("the calendar events were written after the message, in %d separate statements", stored.lateCalendar)
	}
	if stored.versions != 1 || stored.commits != 1 {
		t.Errorf("%d version inserts, %d commits; want one of each, together", stored.versions, stored.commits)
	}

	failed := &calendarDeliveryDB{failVersions: true}
	rr := deliverInvite(t, failed)
	if rr.Code != http.StatusInternalServerError || strings.Contains(rr.Body.String(), "private") {
		t.Errorf("a version insert that failed: %d %s, want a sanitized 500 the gateway retries", rr.Code, rr.Body.String())
	}
	if failed.commits != 0 {
		t.Errorf("a version insert that failed: %d commits, want the delivery rolled back", failed.commits)
	}
}
