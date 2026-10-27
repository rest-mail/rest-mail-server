package handlers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/restmail/restmail/internal/attachmentstore"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestForwardAttachmentsPostgresPersistenceQuotaAndRollback(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL forward persistence/concurrency proof runs in CI with DB_HOST; no local database is started")
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
	schema := fmt.Sprintf("forward_proof_%d", time.Now().UnixNano())
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Exec(`DROP SCHEMA "` + schema + `" CASCADE`).Error })
	dialector := base.Dialector.(*postgres.Dialector)
	connection, err := pgx.ParseConfig(dialector.Config.DSN)
	if err != nil {
		t.Fatal("could not derive isolated test connection")
	}
	connection.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*connection)
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.QuotaUsage{}, &models.Message{}, &models.Attachment{}, &models.OutboundQueue{}, &models.Contact{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "forward.example.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	sender := models.Mailbox{DomainID: domain.ID, LocalPart: "sender", Address: "sender@" + domain.Name, Active: true, QuotaBytes: 1 << 20}
	if err := db.Create(&sender).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.QuotaUsage{MailboxID: sender.ID}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageHandler(db, nil, nil, "", nil)
	h.forwardRoot = t.TempDir()
	h.SetForwardStorage(8192, nil)
	data := []byte("invoice\xff\x00")
	parts := []pipeline.Attachment{{Filename: "invoice.bin", ContentType: "application/octet-stream", Disposition: "attachment", Content: base64.StdEncoding.EncodeToString(data), Size: int64(len(data))}, {Filename: "empty.txt", ContentType: "text/plain", Disposition: "attachment", Size: 0}}
	raw, err := rmime.Serialize(&pipeline.EmailJSON{Headers: pipeline.Headers{From: []pipeline.Address{{Address: "old@example.test"}}, Subject: "receipt"}, Body: pipeline.Body{ContentType: "text/plain", Content: "receipt"}, Attachments: parts})
	if err != nil {
		t.Fatal(err)
	}
	original := models.Message{MailboxID: sender.ID, Sender: "old@example.test", Subject: "receipt", BodyText: "receipt", RawMessage: string(raw), RawSize: len(raw), HasAttachments: true, MsgID: "<original@example.test>"}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	var originalRows []models.Attachment
	for _, part := range parts {
		source, _ := base64.StdEncoding.DecodeString(part.Content)
		ref, sum, err := attachmentstore.PutFilesystem(t.Context(), h.forwardRoot, source)
		if err != nil {
			t.Fatal(err)
		}
		row := models.Attachment{MessageID: original.ID, Filename: part.Filename, ContentType: part.ContentType, SizeBytes: part.Size, StorageType: "filesystem", StorageRef: ref, Checksum: sum}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		originalRows = append(originalRows, row)
	}
	r := httptest.NewRequest("POST", "/forward", nil)
	loaded, err := h.readForwardParts(r, &original)
	if err != nil {
		t.Fatal(err)
	}
	req := sendMessageRequest{From: sender.Address, To: []string{"target@remote.test"}, Subject: "Fwd: receipt", BodyText: "forwarded"}
	makeSent := func(id string) *models.Message {
		return &models.Message{MailboxID: sender.ID, Folder: "Sent", MsgID: id, Sender: sender.Address, Subject: req.Subject, BodyText: req.BodyText, SizeBytes: len(req.Subject) + len(req.BodyText), IsRead: true, ReceivedAt: time.Now()}
	}
	first := makeSent("<forward@example.test>")
	if result, err := h.commitForward(r, req, &sender, first, loaded, nil, nil); err != nil || result.Code != 201 {
		t.Fatalf("forward: result=%+v err=%v", result, err)
	}
	var stored []models.Attachment
	if err := db.Where("message_id = ?", first.ID).Order("id").Find(&stored).Error; err != nil || len(stored) != 2 {
		t.Fatalf("stored parts: %+v %v", stored, err)
	}
	for i, row := range stored {
		if row.StorageRef == originalRows[i].StorageRef {
			t.Fatal("forward shares original file")
		}
		got, err := os.ReadFile(row.StorageRef)
		want, _ := base64.StdEncoding.DecodeString(parts[i].Content)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("stored copy bytes changed")
		}
	}
	var queue models.OutboundQueue
	if err := db.Where("message_id = ?", first.ID).First(&queue).Error; err != nil || queue.RawMessage != first.RawMessage {
		t.Fatalf("queue source differs: %v", err)
	}
	parsed, err := rmime.Parse([]byte(queue.RawMessage))
	if err != nil || len(parsed.Attachments) != 2 {
		t.Fatal("queued parts lost")
	}
	cost := int64(first.SizeBytes) + int64(len(data))
	if err := db.Model(&sender).Update("quota_bytes", cost*3-1).Error; err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	for i := range 2 {
		go func() {
			<-start
			_, err := h.commitForward(r, req, &sender, makeSent(fmt.Sprintf("<parallel-%d@example.test>", i)), loaded, nil, nil)
			results <- err
		}()
	}
	close(start)
	success, refused := 0, 0
	for range 2 {
		if err := <-results; err == nil {
			success++
		} else if strings.Contains(err.Error(), "insufficient quota") {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || refused != 1 {
		t.Fatalf("concurrent quota: success=%d refused=%d", success, refused)
	}
	var before models.Mailbox
	if err := db.First(&before, sender.ID).Error; err != nil || before.QuotaUsedBytes != cost*2 {
		t.Fatalf("sender quota: %+v %v", before, err)
	}
	if err := db.Model(&sender).Update("quota_bytes", int64(1<<20)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE FUNCTION reject_forward_queue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private queue failure'; END; $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_forward_queue BEFORE INSERT ON outbound_queue FOR EACH ROW EXECUTE FUNCTION reject_forward_queue()`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := h.commitForward(r, req, &sender, makeSent("<rollback@example.test>"), loaded, nil, nil); err == nil {
		t.Fatal("queue fault acknowledged a forward")
	}
	var count int64
	if err := db.Model(&models.Message{}).Where("folder = ?", "Sent").Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("partial Sent row: %d %v", count, err)
	}
	if err := db.Model(&models.Attachment{}).Count(&count).Error; err != nil || count != 6 {
		t.Fatalf("partial attachment reference: %d %v", count, err)
	}
	var after models.Mailbox
	if err := db.First(&after, sender.ID).Error; err != nil || after.QuotaUsedBytes != before.QuotaUsedBytes {
		t.Fatalf("rollback changed quota: %+v %v", after, err)
	}
}
