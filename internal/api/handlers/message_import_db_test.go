package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/restmail/restmail/internal/api/endpoint"
	apitypes "github.com/restmail/restmail/internal/api/types"
	"github.com/restmail/restmail/internal/bootstrap"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMessageImportPostgresConcurrencySourceQuotaAndAuditRollback(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL import persistence/concurrency proof runs in CI with DB_HOST; no local database is started")
	}
	base, err := rmdb.Connect(testDBConfig())
	if err != nil {
		t.Fatal("could not connect to the configured PostgreSQL test database")
	}
	basePool, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = basePool.Close() })
	schema := fmt.Sprintf("import_proof_%d", time.Now().UnixNano())
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error })
	dialector, ok := base.Dialector.(*postgres.Dialector)
	if !ok {
		t.Fatal("PostgreSQL dialector required")
	}
	connection, err := pgx.ParseConfig(dialector.Config.DSN)
	if err != nil {
		t.Fatal("could not derive the isolated test connection")
	}
	connection.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*connection)
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(append(accountModels, &models.Message{}, &models.Attachment{})...); err != nil {
		t.Fatal(err)
	}
	if err := rmdb.MigrateAuditLog(db); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
		t.Fatal(err)
	}
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageImportHandler(db, 4096, nil)
	h.directory = t.TempDir()
	router := importMessageRoutes(h)
	raw := []byte("Message-ID: <binary-history@example.test>\r\nFrom: Original <old@example.test>\r\nSubject: original\r\nAuthentication-Results: previous.example; spf=pass\r\nContent-Type: multipart/mixed; boundary=OLD\r\n\r\n--OLD\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\nbody\xff\x00\r\n--OLD\r\nContent-Type: application/octet-stream; name=empty.bin\r\nContent-Disposition: attachment; filename=empty.bin\r\nContent-Transfer-Encoding: base64\r\n\r\n\r\n--OLD--\r\n")
	date := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	body := apitypes.ImportMessageBody{RawMessage: raw, Folder: "Archive", InternalDate: date, Flags: []string{`\Seen`, `\Answered`, `\Flagged`, `\Draft`}}
	call := func(message apitypes.ImportMessageBody) *httptest.ResponseRecorder {
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fx.user.ID), fx.admin.ID, nil, string(encoded))
		request.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, request)
		return w
	}
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { <-start; results <- call(body) }()
	}
	close(start)
	imported, skipped := 0, 0
	var id uint
	for range 2 {
		w := <-results
		if w.Code != 200 {
			t.Fatalf("concurrent import: %d %s", w.Code, w.Body)
		}
		var result endpoint.Data[apitypes.ImportMessageResult]
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if id != 0 && id != result.Data.ID {
			t.Fatal("concurrent copies received different identities")
		}
		id = result.Data.ID
		if result.Data.Imported {
			imported++
		} else {
			skipped++
		}
	}
	if imported != 1 || skipped != 1 {
		t.Fatalf("concurrent results: imported=%d skipped=%d", imported, skipped)
	}
	var stored models.Message
	if err := db.First(&stored, id).Error; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored.Source(), raw) || stored.RawSize != len(raw) || stored.RawMessage != "" || !stored.ReceivedAt.Equal(date) || !stored.IsRead || !stored.IsAnswered || !stored.IsFlagged || !stored.IsDraft {
		t.Fatalf("source/metadata changed: %+v", stored)
	}
	var attachments []models.Attachment
	if err := db.Where("message_id = ?", id).Find(&attachments).Error; err != nil || len(attachments) != 1 {
		t.Fatalf("empty attachment: %v %+v", err, attachments)
	}
	data, err := os.ReadFile(attachments[0].StorageRef)
	if err != nil || len(data) != 0 || attachments[0].SizeBytes != 0 {
		t.Fatalf("empty attachment lost: %q %v", data, err)
	}
	var usage models.QuotaUsage
	if err := db.Where("mailbox_id = ?", fx.user.ID).First(&usage).Error; err != nil || usage.MessageCount != 1 {
		t.Fatalf("duplicate quota: %+v %v", usage, err)
	}
	var account models.Mailbox
	if err := db.First(&account, fx.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	beforeQuota := account.QuotaUsedBytes
	// Refusing a mandatory success-audit insert must roll back the message,
	// attachment reference and both quota counters, not acknowledge partial mail.
	if err := db.Exec(`CREATE FUNCTION reject_import_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action = 'messages.import' THEN RAISE EXCEPTION 'private audit failure'; END IF; RETURN NEW; END; $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_import_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_import_audit()`).Error; err != nil {
		t.Fatal(err)
	}
	body.RawMessage = bytes.Replace(raw, []byte("binary-history@"), []byte("rollback-history@"), 1)
	w := call(body)
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("audit refusal: %d %s", w.Code, w.Body)
	}
	var count int64
	if err := db.Model(&models.Message{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failed import retained a message: %d %v", count, err)
	}
	if err := db.Model(&models.Attachment{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failed import retained metadata: %d %v", count, err)
	}
	if err := db.First(&account, fx.user.ID).Error; err != nil || account.QuotaUsedBytes != beforeQuota {
		t.Fatalf("audit refusal changed quota: %+v %v", account, err)
	}
	if err := db.Where("mailbox_id = ?", fx.user.ID).First(&usage).Error; err != nil || usage.MessageCount != 1 {
		t.Fatalf("audit refusal changed counters: %+v %v", usage, err)
	}
}
