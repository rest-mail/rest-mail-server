package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/restmail/restmail/internal/auth"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func draftSendPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL draft send proof needs DB_HOST")
	}
	base, err := rmdb.Connect(testDBConfig())
	if err != nil {
		t.Fatal("could not connect to configured PostgreSQL test database")
	}
	basePool, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = basePool.Close() })
	schema := fmt.Sprintf("draft_send_proof_%d", time.Now().UnixNano())
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error })
	connection, err := pgx.ParseConfig(base.Dialector.(*postgres.Dialector).Config.DSN)
	if err != nil {
		t.Fatal("could not derive isolated test connection")
	}
	connection.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*connection)
	pool.SetMaxOpenConns(6)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Message{}, &models.OutboundQueue{}, &models.Contact{}, &models.QuotaUsage{}, &models.Attachment{}, &models.Pipeline{}, &models.SieveScript{}); err != nil {
		t.Fatal(err)
	}
	return db
}

type draftSendObserver struct {
	check  func()
	events int
	pushes int
	traces int
}

func (o *draftSendObserver) Publish(uint, SSEEvent) { o.check(); o.events++ }
func (o *draftSendObserver) NewMail(uint)           { o.check(); o.pushes++ }
func (o *draftSendObserver) Record(models.MessageTrace) {
	o.check()
	o.traces++
}

func TestSendDraftPostgresRollbackNotificationsAndConcurrentSend(t *testing.T) {
	db := draftSendPostgres(t)
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	domain := models.Domain{Name: "draft-send.example.test", Active: true}
	must(t, db.Create(&domain).Error)
	sender := models.Mailbox{DomainID: domain.ID, LocalPart: "sender", Address: "sender@" + domain.Name, Active: true}
	recipient := models.Mailbox{DomainID: domain.ID, LocalPart: "recipient", Address: "recipient@" + domain.Name, Active: true}
	must(t, db.Create(&sender).Error)
	must(t, db.Create(&recipient).Error)
	must(t, db.Create(&models.QuotaUsage{MailboxID: recipient.ID}).Error)
	for _, direction := range []string{"outbound", "inbound"} {
		must(t, db.Create(&models.Pipeline{DomainID: domain.ID, Direction: direction, Active: true, Filters: []byte(`[]`)}).Error)
	}
	engine := pipeline.NewEngine(pipeline.NewRegistry(), nil)
	seed := func(t *testing.T, subject string) models.Message {
		draft := models.Message{MailboxID: sender.ID, Folder: "Drafts", Sender: sender.Address, Subject: subject, BodyText: "hello", IsDraft: true, RecipientsTo: models.JSONB(`["far@remote.example","` + recipient.Address + `"]`), RecipientsCc: models.JSONB(`[]`)}
		must(t, db.Create(&draft).Error)
		return draft
	}
	send := func(ctx context.Context, h *MessageHandler, id uint) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/messages/draft/%d/send", id), nil).WithContext(ctx)
		r = withClaims(r, &auth.Claims{AccountID: sender.ID, Address: sender.Address, CredentialKind: "app_password"})
		rr := httptest.NewRecorder()
		composeRoutes(h).ServeHTTP(rr, r)
		return rr
	}
	count := func(t *testing.T, model any, where string, args ...any) int64 {
		var n int64
		must(t, db.Model(model).Where(where, args...).Count(&n).Error)
		return n
	}
	assertState := func(t *testing.T, draft models.Message, committed bool) {
		t.Helper()
		wantDraft, wantSent := int64(1), int64(0)
		if committed {
			wantDraft, wantSent = 0, 1
		}
		if n := count(t, &models.Message{}, "id = ? AND is_draft = ?", draft.ID, true); n != wantDraft {
			t.Fatalf("draft count=%d want=%d", n, wantDraft)
		}
		for _, folder := range []string{"Sent", "INBOX"} {
			if n := count(t, &models.Message{}, "subject = ? AND folder = ?", draft.Subject, folder); n != wantSent {
				t.Fatalf("%s copies=%d want=%d", folder, n, wantSent)
			}
		}
		if n := count(t, &models.OutboundQueue{}, "raw_message LIKE ?", "%Subject: "+draft.Subject+"\r\n%"); n != wantSent {
			t.Fatalf("queued copies=%d want=%d", n, wantSent)
		}
	}
	for _, stage := range []string{"delete", "queue", "local", "cancel", "delete_once", "success"} {
		t.Run(stage, func(t *testing.T) {
			draft := seed(t, stage)
			observer := &draftSendObserver{check: func() { assertState(t, draft, true) }}
			h := NewMessageHandler(db, nil, engine, "", observer)
			h.broker, h.newMail = observer, observer
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			if stage == "delete" || stage == "delete_once" || stage == "queue" || stage == "local" {
				table, operation, condition, returned := "messages", "DELETE", fmt.Sprintf("OLD.id = %d", draft.ID), "OLD"
				if stage == "delete_once" {
					must(t, db.Exec("CREATE SEQUENCE draft_send_attempts").Error)
					condition += " AND nextval('draft_send_attempts') = 1"
					defer func() { must(t, db.Exec("DROP SEQUENCE draft_send_attempts").Error) }()
				}
				if stage == "queue" {
					table, operation, condition, returned = "outbound_queue", "INSERT", "true", "NEW"
				}
				if stage == "local" {
					operation, condition, returned = "INSERT", "NEW.folder = 'INBOX'", "NEW"
				}
				must(t, db.Exec(fmt.Sprintf(`CREATE FUNCTION draft_send_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'private storage failure'; END IF; RETURN %s; END $$`, condition, returned)).Error)
				must(t, db.Exec(fmt.Sprintf(`CREATE TRIGGER draft_send_fault BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION draft_send_fault()`, operation, table)).Error)
				defer func() {
					must(t, db.Exec("DROP TRIGGER draft_send_fault ON "+table).Error)
					must(t, db.Exec("DROP FUNCTION draft_send_fault()").Error)
				}()
			}
			if stage == "cancel" {
				must(t, db.Callback().Delete().Before("gorm:delete").Register("cancel_draft_send", func(q *gorm.DB) {
					cancel()
					_ = q.AddError(context.Canceled)
				}))
				defer func() { must(t, db.Callback().Delete().Remove("cancel_draft_send")) }()
			}
			rr := send(ctx, h, draft.ID)
			if stage != "success" && stage != "delete_once" {
				if rr.Code < 500 || strings.Contains(rr.Body.String(), "private") {
					t.Fatalf("failure status=%d body=%s", rr.Code, rr.Body)
				}
				assertState(t, draft, false)
				if observer.events != 0 || observer.pushes != 0 || observer.traces != 0 {
					t.Fatalf("uncommitted send notified clients: %+v", observer)
				}
				return
			}
			if rr.Code != http.StatusCreated {
				t.Fatalf("send status=%d body=%s", rr.Code, rr.Body)
			}
			assertState(t, draft, true)
			if observer.events != 3 || observer.pushes != 1 || observer.traces != 2 {
				t.Fatalf("committed send notifications: %+v", observer)
			}
		})
	}
	t.Run("concurrent", func(t *testing.T) {
		draft := seed(t, "concurrent")
		h := NewMessageHandler(db, nil, engine, "", nil)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		start, results := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
		for range 2 {
			go func() { <-start; results <- send(ctx, h, draft.ID) }()
		}
		close(start)
		statuses := map[int]int{}
		for range 2 {
			rr := <-results
			statuses[rr.Code]++
		}
		if statuses[http.StatusCreated] != 1 || statuses[http.StatusNotFound] != 1 {
			t.Fatalf("concurrent send statuses=%v", statuses)
		}
		assertState(t, draft, true)
	})
}
