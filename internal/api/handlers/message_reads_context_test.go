package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
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

// Listing a folder's messages, reading a thread, listing folders and calendar
// events and reading the quota run their SQL — the account access check first
// — with the HTTP request's context (#1163).

type messageReadsKey struct{}

type messageReadsDB struct {
	serviceProbeTestDB
	mu         sync.Mutex
	seen, lost []string
}

func (d *messageReadsDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *messageReadsDB) Driver() driver.Driver                        { return d }
func (d *messageReadsDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *messageReadsDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(messageReadsKey{}) != true {
		d.lost = append(d.lost, q)
	}
	switch {
	case strings.HasPrefix(q, "SELECT count("):
		return &deliveryReadRows{columns: []string{"count"}, values: []driver.Value{int64(0)}}, nil
	case strings.Contains(q, `FROM "mailboxes"`):
		return &deliveryReadRows{columns: []string{"id", "address", "active", "domain_id", "quota_bytes"},
			values: []driver.Value{int64(7), "owner@example.test", true, int64(3), int64(1 << 30)}}, nil
	}
	return &deliveryReadRows{}, nil
}

func TestMessageReadRoutesCarryTheRequestContext(t *testing.T) {
	for _, route := range []struct{ name, path string }{
		{"listMessages", "/api/v1/accounts/7/folders/INBOX/messages"},
		{"getThread", "/api/v1/accounts/7/threads/t-1"},
		{"listFolders", "/api/v1/accounts/7/folders"},
		{"listCalendarEvents", "/api/v1/accounts/7/calendar-events"},
		{"getQuota", "/api/v1/accounts/7/quota"},
	} {
		t.Run(route.name, func(t *testing.T) {
			d := &messageReadsDB{}
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
			h.RegisterFolders(rs)
			h.RegisterCompose(rs)

			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), messageReadsKey{}, true), 4*time.Second)
			defer cancel()
			r := withClaims(httptest.NewRequest(http.MethodGet, route.path, nil).WithContext(ctx),
				&auth.Claims{AccountID: 7, Address: "owner@example.test", CredentialID: 3, CredentialKind: "app_password"})
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, r)

			if len(d.seen) < 2 {
				t.Fatalf("only %d statements reached the driver (status %d %s)", len(d.seen), rr.Code, rr.Body.String())
			}
			if len(d.lost) > 0 {
				t.Errorf("%d of %d statements ran without the request's context (status %d), first %q",
					len(d.lost), len(d.seen), rr.Code, d.lost[0])
			}
		})
	}
}
