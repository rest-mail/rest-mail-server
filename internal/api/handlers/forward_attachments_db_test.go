package handlers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm/logger"
)

func TestForwardAttachmentsPostgresPersistenceQuotaAndRollback(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("PostgreSQL forward persistence/concurrency proof runs in CI with DB_HOST; no local database is started")
	}
	db := openScratchDB(t, "forwardproof", []any{withForeignKeys, &models.Domain{}, &models.Mailbox{}, &models.QuotaUsage{}, &models.Message{}, &models.Attachment{}, &models.OutboundQueue{}, &models.Contact{}})
	db.Logger = logger.Default.LogMode(logger.Silent)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(4)
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

// A forward with its own attachment copies delivers to a mailbox on this server
// inside its transaction. When that recipient's inbound pipeline refuses the
// message (a DMARC reject of the sender's domain, say), the forward is answered
// for the message it sent, as a send is: its Sent copy and the remote
// recipient's queue entry are kept, and the refused recipient gets nothing.
func TestForwardAttachmentsLocalRefusalKeepsTheForward(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := migrateModels(db, &models.QuotaUsage{}, &models.Pipeline{}, &models.SieveScript{}, &models.OutboundQueue{}, &models.Contact{}); err != nil {
		t.Skipf("forward DB test skipped: migrate failed (%v)", err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	if err := tx.Where(models.QuotaUsage{MailboxID: fx.mailbox.ID}).FirstOrCreate(&models.QuotaUsage{MailboxID: fx.mailbox.ID}).Error; err != nil {
		t.Fatal(err)
	}
	filters, err := json.Marshal([]pipeline.FilterConfig{{Name: "refuse_everything", Type: pipeline.FilterTypeAction, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.Pipeline{DomainID: fx.mailbox.DomainID, Direction: "inbound", Active: true, Filters: filters}).Error; err != nil {
		t.Fatal(err)
	}
	registry := withQuietVacation(pipeline.NewRegistry())
	registry.Register("refuse_everything", func([]byte) (pipeline.Filter, error) { return refuseEverything{}, nil })
	h := NewMessageHandler(tx, nil, pipeline.NewEngine(registry, nil), "", nil)
	h.forwardRoot = t.TempDir()
	h.SetForwardStorage(8192, nil)

	req := sendMessageRequest{From: fx.mailbox.Address, To: []string{fx.mailbox.Address, "target@remote.test"}, Subject: "Fwd: receipt", BodyText: "forwarded"}
	sent := &models.Message{MailboxID: fx.mailbox.ID, Folder: "Sent", MsgID: "<refused-forward@delq.test>", Sender: fx.mailbox.Address, Subject: req.Subject, BodyText: req.BodyText, SizeBytes: len(req.Subject) + len(req.BodyText), IsRead: true, ReceivedAt: time.Now()}
	r := httptest.NewRequest("POST", "/forward", nil)
	result, err := h.commitForward(r, req, &fx.mailbox, sent, nil, nil, nil)
	if err != nil || result.Code != 201 {
		t.Fatalf("forward to a refusing local recipient: result=%+v err=%v", result, err)
	}
	var count int64
	if err := tx.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ?", fx.mailbox.ID, "Sent").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("Sent copy: %d %v", count, err)
	}
	if err := tx.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ?", fx.mailbox.ID, "INBOX").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("the refused recipient got the message: %d %v", count, err)
	}
	var queued []models.OutboundQueue
	if err := tx.Where("message_id = ?", sent.ID).Find(&queued).Error; err != nil || len(queued) != 1 || queued[0].Recipient != "target@remote.test" {
		t.Fatalf("remote queue entry: %+v %v", queued, err)
	}
}

func TestLocalRecipientRefused(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.New("rejected: 550 DMARC policy reject for example.com"), true},
		{errors.New("deferred: greylisted"), true},
		{errors.New("mailbox_full: over quota"), true},
		{errors.New("insert message: connection reset"), false},
		{fmt.Errorf("wrapped: %w", errors.New("rejected: 550")), false},
	} {
		if got := localRecipientRefused(tc.err); got != tc.want {
			t.Errorf("localRecipientRefused(%q) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
