package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/attachmentstore/s3test"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

func TestS3AttachmentDeliveryMetadataFailureRollsBack(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	if err := db.AutoMigrate(&models.QuotaUsage{}, &models.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	usage := models.QuotaUsage{MailboxID: fx.mailbox.ID}
	if err := tx.Create(&usage).Error; err != nil {
		t.Fatal(err)
	}
	fc, err := json.Marshal([]pipeline.FilterConfig{{Name: "extract_attachments", Type: pipeline.FilterTypeTransform, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Create(&models.Pipeline{DomainID: fx.mailbox.DomainID, Direction: "inbound", Active: true, Filters: fc}).Error; err != nil {
		t.Fatal(err)
	}
	fake := s3test.New(t)
	registry := pipeline.NewRegistry()
	registry.Register("extract_attachments", filters.NewExtractAttachmentsWithS3(attachmentstore.NewS3(fake.Config)))
	h := NewMessageHandler(tx, nil, pipeline.NewEngine(registry, nil), "", nil)
	raw := "From: sender@remote.test\r\nTo: owner@delq.test\r\nSubject: attachment\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=part\r\n\r\n--part\r\nContent-Type: text/plain\r\n\r\nbody\r\n--part\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=test.bin\r\nContent-Transfer-Encoding: base64\r\n\r\ncGF5bG9hZA==\r\n--part--\r\n"
	params := localDeliveryParams{Mailbox: fx.mailbox, Sender: "sender@remote.test", Subject: "attachment", BodyText: "body", RawMessage: raw}
	const callback = "s3-test-attachment-write-failure"
	if err := tx.Callback().Create().Before("gorm:create").Register(callback, func(q *gorm.DB) {
		if q.Statement.Table == "attachments" {
			_ = q.AddError(errors.New("metadata unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Callback().Create().Remove(callback) })
	if _, err := h.deliverToLocal(context.Background(), params); err == nil {
		t.Fatal("delivery accepted without attachment metadata")
	}
	if countMessages(t, tx, fx.mailbox.ID) != 0 || quotaUsedBytes(t, tx, fx.mailbox.ID) != 0 {
		t.Fatal("failed attachment write partially committed delivery")
	}
	if err := tx.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	msg, err := h.deliverToLocal(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	var att models.Attachment
	if err := tx.Where("message_id = ?", msg.ID).First(&att).Error; err != nil || att.StorageType != "s3" || !msg.HasAttachments {
		t.Fatalf("retried attachment metadata: %+v %v", att, err)
	}
	if err := tx.First(&usage, fx.mailbox.ID).Error; err != nil || usage.AttachmentBytes != 7 || usage.MessageCount != 1 {
		t.Fatalf("retried quota: %+v %v", usage, err)
	}
	if msg.RawMessage != raw {
		t.Fatal("original MIME bytes changed during attachment extraction")
	}
}
