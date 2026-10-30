package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/attachmentstore/s3test"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

func TestOwnedQuotaIncludesAttachmentBytes(t *testing.T) {
	db := openAccountsTestDB(t)
	if err := db.AutoMigrate(&models.Attachment{}, &models.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	fx := seedAccountsFixture(t, db)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	if err := tx.Model(&fx.admin).Updates(map[string]any{"quota_bytes": 100, "quota_used_bytes": 95}).Error; err != nil {
		t.Fatal(err)
	}
	child := quotaChild(t, tx, fx.admin, "attached", 0, 0)
	fc, _ := json.Marshal([]pipeline.FilterConfig{{Name: "extract_attachments", Type: pipeline.FilterTypeTransform, Enabled: true}})
	if err := tx.Create(&models.Pipeline{DomainID: fx.domain.ID, Direction: "inbound", Active: true, Filters: fc}).Error; err != nil {
		t.Fatal(err)
	}
	fake := s3test.New(t)
	registry := pipeline.NewRegistry()
	registry.Register("extract_attachments", filters.NewExtractAttachmentsWithS3(attachmentstore.NewS3(fake.Config)))
	h := NewMessageHandler(tx, nil, pipeline.NewEngine(registry, nil), "", nil)
	raw := "From: sender@remote.test\r\nSubject: x\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=part\r\n\r\n--part\r\nContent-Type: text/plain\r\n\r\nx\r\n--part\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=test.bin\r\nContent-Transfer-Encoding: base64\r\n\r\ncGF5bG9hZA==\r\n--part--\r\n"
	params := localDeliveryParams{Mailbox: child, Sender: "sender@remote.test", Subject: "x", BodyText: "x", RawMessage: raw}
	if _, err := h.deliverToLocal(context.Background(), params); err == nil || !strings.HasPrefix(err.Error(), "mailbox_full:") {
		t.Fatalf("attachment quota: %v", err)
	}
	if countMessages(t, tx, child.ID) != 0 || quotaUsedBytes(t, tx, child.ID) != 0 {
		t.Fatal("refused delivery wrote a message or charged quota")
	}
	if err := tx.Model(&fx.admin).Update("quota_used_bytes", 91).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := h.deliverToLocal(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if used := quotaUsedBytes(t, tx, child.ID); used != 9 {
		t.Fatalf("charged %d, want 9 including attachment", used)
	}
}

func TestRESTMAILOwnedQuota(t *testing.T) {
	db, owner := restmailStorageFixture(t)
	child := quotaChild(t, db, owner, "agent", 0, 0)
	cost := int64(len("storage") + len("héllo") + len("<p>héllo</p>"))
	if err := db.Model(&owner).Update("quota_bytes", 13+cost-1).Error; err != nil {
		t.Fatal(err)
	}
	rr := restmailStorageRequest(t, db, context.Background(), []string{child.Address})
	if rr.Code != http.StatusUnprocessableEntity || errorCode(rr) != "undeliverable" {
		t.Fatalf("RESTMAIL shared quota: %d %s", rr.Code, rr.Body.String())
	}
	if countMessages(t, db, child.ID) != 0 || quotaUsedBytes(t, db, child.ID) != 0 {
		t.Fatal("refused RESTMAIL delivery changed storage")
	}
	if err := db.Model(&owner).Update("quota_bytes", 13+cost).Error; err != nil {
		t.Fatal(err)
	}
	rr = restmailStorageRequest(t, db, context.Background(), []string{child.Address})
	if rr.Code != http.StatusCreated {
		t.Fatalf("RESTMAIL exact boundary: %d %s", rr.Code, rr.Body.String())
	}
	if used := quotaUsedBytes(t, db, child.ID); used != cost {
		t.Fatalf("charged %d, want %d", used, cost)
	}
}
