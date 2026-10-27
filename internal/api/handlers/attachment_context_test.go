package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Listing and downloading attachments run their SQL — the message or
// attachment lookup, the mailbox and the access check — with the HTTP
// request's context (#1163). They ran on the background connection, so a
// client that went away left those reads running past the request's
// deadline.

type attachmentContextKey struct{}

type attachmentContextDB struct {
	serviceProbeTestDB
	mu         sync.Mutex
	seen, lost []string
}

func (d *attachmentContextDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *attachmentContextDB) Driver() driver.Driver                        { return d }
func (d *attachmentContextDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *attachmentContextDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(attachmentContextKey{}) != true {
		d.lost = append(d.lost, q)
	}
	return &deliveryReadRows{}, nil
}

func TestAttachmentRoutesCarryTheRequestContext(t *testing.T) {
	for _, path := range []string{"/api/v1/messages/9/attachments", "/api/v1/attachments/9"} {
		t.Run(path, func(t *testing.T) {
			d := &attachmentContextDB{}
			pool := sql.OpenDB(d)
			t.Cleanup(func() { _ = pool.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
				&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			router := chi.NewRouter()
			NewAttachmentHandler(db, nil).Register(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})

			ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), attachmentContextKey{}, true), 4*time.Second)
			defer cancel()
			r := withClaims(httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx), mailboxClaims(7, "reader@example.test"))
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, r)

			if rr.Code != http.StatusNotFound {
				t.Errorf("status %d (%s), want 404 for a row that is not there", rr.Code, rr.Body.String())
			}
			if len(d.seen) == 0 {
				t.Fatal("no SQL reached the driver")
			}
			if len(d.lost) > 0 {
				t.Errorf("%d of %d statements ran without the request's context, first %q", len(d.lost), len(d.seen), d.lost[0])
			}
		})
	}
}
