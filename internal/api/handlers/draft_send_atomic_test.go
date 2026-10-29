package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type draftSendState struct {
	draft        bool
	sent, queued int
}

// Model committed and uncommitted rows separately: a 201 with a queue row
// and a surviving draft is the duplicate-send bug, even when it was logged.
type draftSendDB struct {
	serviceProbeTestDB
	stored, pending                         draftSendState
	inTx, failDelete, failQueue, failCommit bool
	failRaw                                 bool
	locked                                  bool
}

func (d *draftSendDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *draftSendDB) Driver() driver.Driver                        { return d }
func (d *draftSendDB) Open(string) (driver.Conn, error)             { return d, nil }
func (d *draftSendDB) state() *draftSendState {
	if d.inTx {
		return &d.pending
	}
	return &d.stored
}
func (d *draftSendDB) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	s := d.state()
	if rows, ok := defaultGrantRows(q); ok {
		return rows, nil
	}
	switch {
	case strings.Contains(q, `FROM "messages"`):
		d.locked = d.locked || strings.Contains(q, "FOR UPDATE")
		if !s.draft {
			return &deliveryReadRows{}, nil
		}
		return &deliveryReadRows{columns: []string{"id", "mailbox_id", "sender", "subject", "is_draft", "recipients_to", "recipients_cc"}, values: []driver.Value{int64(5), int64(7), "owner@example.test", "hello", true, []byte(`["far@remote.example"]`), []byte(`[]`)}}, nil
	case strings.HasPrefix(q, `INSERT INTO "messages"`):
		s.sent++
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(60)}}, nil
	case strings.HasPrefix(q, `INSERT INTO "outbound_queue"`):
		if d.failQueue {
			return nil, errors.New("private queue failure")
		}
		s.queued++
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(70)}}, nil
	case strings.Contains(q, `FROM "mailboxes"`):
		for _, a := range args {
			if a.Value == "far@remote.example" {
				return &deliveryReadRows{}, nil
			}
		}
		return &deliveryReadRows{columns: []string{"id", "address", "active", "domain_id"}, values: []driver.Value{int64(7), "owner@example.test", true, int64(3)}}, nil
	case strings.Contains(q, `FROM "domains"`):
		return &deliveryReadRows{columns: []string{"id", "name", "active", "sending_policy"}, values: []driver.Value{int64(3), "example.test", true, ""}}, nil
	case strings.HasPrefix(q, "INSERT"):
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(80)}}, nil
	}
	return &deliveryReadRows{}, nil
}
func (d *draftSendDB) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, `DELETE FROM "messages"`) {
		if d.failDelete {
			return nil, errors.New("private draft deletion failure")
		}
		d.state().draft = false
	}
	if d.failRaw && strings.HasPrefix(q, `UPDATE "messages"`) {
		return nil, errors.New("private raw storage failure")
	}
	return driver.RowsAffected(1), nil
}
func (d *draftSendDB) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	d.pending, d.inTx = d.stored, true
	return &draftSendTx{d}, nil
}

type draftSendTx struct{ d *draftSendDB }

func (tx *draftSendTx) Commit() error {
	tx.d.inTx = false
	if tx.d.failCommit {
		return errors.New("private commit failure")
	}
	tx.d.stored = tx.d.pending
	return nil
}
func (tx *draftSendTx) Rollback() error { tx.d.inTx = false; return nil }

func draftSendHandler(t *testing.T, d *draftSendDB) *MessageHandler {
	t.Helper()
	d.stored.draft = true
	pool := sql.OpenDB(d)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return NewMessageHandler(db, nil, nil, "", nil)
}

func sendAtomicDraft(h *MessageHandler) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/messages/draft/5/send", nil)
	r = r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: 7, Address: "owner@example.test", CredentialID: 3, CredentialKind: "app_password"}))
	rr := httptest.NewRecorder()
	composeRoutes(h).ServeHTTP(rr, r)
	return rr
}

func TestSendDraftCommitsRemovalWithSentAndQueue(t *testing.T) {
	for _, stage := range []string{"delete", "queue", "raw", "commit", "success"} {
		t.Run(stage, func(t *testing.T) {
			d := &draftSendDB{failDelete: stage == "delete", failQueue: stage == "queue", failRaw: stage == "raw"}
			h := draftSendHandler(t, d)
			// Commit failure is enabled only after setup, which performs no writes.
			d.failCommit = stage == "commit"
			rr := sendAtomicDraft(h)
			if stage != "success" {
				if rr.Code < 500 || d.stored != (draftSendState{draft: true}) {
					t.Fatalf("%s failure: status=%d body=%s committed=%+v; want error, original draft, no Sent or queue rows", stage, rr.Code, rr.Body, d.stored)
				}
				if strings.Contains(rr.Body.String(), "private") {
					t.Fatalf("storage details leaked: %s", rr.Body)
				}
				d.failDelete, d.failQueue, d.failRaw, d.failCommit = false, false, false, false
				rr = sendAtomicDraft(h)
			}
			if rr.Code != http.StatusCreated || d.stored != (draftSendState{sent: 1, queued: 1}) {
				t.Fatalf("send: status=%d body=%s committed=%+v", rr.Code, rr.Body, d.stored)
			}
			if !d.locked {
				t.Fatal("draft was not locked against concurrent sends")
			}
			if rr := sendAtomicDraft(h); rr.Code != http.StatusNotFound || d.stored.queued != 1 {
				t.Fatalf("second send: status=%d body=%s committed=%+v", rr.Code, rr.Body, d.stored)
			}
		})
	}
}
