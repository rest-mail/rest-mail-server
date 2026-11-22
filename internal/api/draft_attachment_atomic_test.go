package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"gorm.io/gorm"
)

func TestDraftAttachmentRefusedSendRetainsUploadsAndQuota(t *testing.T) {
	for _, mode := range []string{"queue_failure", "quota", "encoded_size", "missing_object"} {
		t.Run(mode, func(t *testing.T) {
			f := newDraftUploadFixture(t, 8192)
			content := "attachment"
			if mode == "encoded_size" {
				content = strings.Repeat("x", 7000)
			}
			row := uploadedAttachment(t, f.upload(t, strings.NewReader(content), "keep.pdf"))
			want := 503
			if mode == "quota" {
				want = 422
				if err := f.db.Model(&models.Mailbox{}).Where("id = ?", f.mailboxID).Update("quota_bytes", len(content)).Error; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "encoded_size" {
				want = 413
			}
			if mode == "missing_object" {
				want = 500
				var stored models.Attachment
				if err := f.db.First(&stored, row.ID).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.store.Delete(t.Context(), stored.StorageRef); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "queue_failure" {
				stopTraceRecorder(f.routers)
				if err := f.db.Callback().Create().Before("gorm:create").Register("draft_upload_queue_failure", func(query *gorm.DB) {
					if query.Statement.Table == "outbound_queue" {
						_ = query.AddError(errors.New("private queue failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = f.db.Callback().Create().Remove("draft_upload_queue_failure") }()
			}
			rr := f.do(http.MethodPost, fmt.Sprintf("/api/v1/messages/draft/%d/send", f.draft.ID), f.session, "")
			if rr.Code != want || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("refused send = %d %s, want %d", rr.Code, rr.Body, want)
			}
			for _, target := range []struct {
				model any
				query string
				want  int64
			}{
				{&models.Message{}, "is_draft = true", 1},
				{&models.Message{}, "folder = 'Sent'", 0},
				{&models.Attachment{}, "message_id = 1", 1},
				{&models.OutboundQueue{}, "true", 0},
			} {
				var count int64
				if err := f.db.Model(target.model).Where(target.query).Count(&count).Error; err != nil || count != target.want {
					t.Fatalf("refusal changed persisted state for %T: %d, %v", target.model, count, err)
				}
			}
			var mailbox models.Mailbox
			var usage models.QuotaUsage
			if err := f.db.First(&mailbox, f.mailboxID).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.First(&usage, "mailbox_id = ?", f.mailboxID).Error; err != nil {
				t.Fatal(err)
			}
			if mailbox.QuotaUsedBytes != int64(len(content)) || usage.AttachmentBytes != int64(len(content)) {
				t.Fatalf("refusal changed quota: %d/%d", mailbox.QuotaUsedBytes, usage.AttachmentBytes)
			}
		})
	}
}

func TestDraftAttachmentConcurrentUploadsCannotOverdrawQuota(t *testing.T) {
	f := newDraftUploadFixture(t, 8192)
	if err := f.db.Model(&models.Mailbox{}).Where("id = ?", f.mailboxID).Update("quota_bytes", 15).Error; err != nil {
		t.Fatal(err)
	}
	statuses := make(chan int, 4)
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			statuses <- f.upload(t, strings.NewReader("attachment"), "concurrent.pdf").Code
		})
	}
	workers.Wait()
	close(statuses)
	accepted := 0
	for status := range statuses {
		if status == 201 {
			accepted++
		} else if status != 422 {
			t.Fatalf("concurrent upload status = %d", status)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d uploads, want one within quota", accepted)
	}
}

func TestDraftAttachmentDraftMoveTransfersQuotaAtomically(t *testing.T) {
	f := newDraftUploadFixture(t, 8192)
	row := uploadedAttachment(t, f.upload(t, strings.NewReader("attachment"), "move.pdf"))
	other := f.seedMailbox(1, "other")
	f.own(other, "other@example.test")
	if err := f.db.Model(&models.Mailbox{}).Where("id = ?", other).Update("quota_bytes", 1).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/messages/draft/%d", f.draft.ID)
	rr := f.do(http.MethodPut, path, f.session, `{"from":"other@example.test"}`)
	if rr.Code != 422 {
		t.Fatalf("move into full account = %d %s", rr.Code, rr.Body)
	}
	if err := f.db.Model(&models.Mailbox{}).Where("id = ?", other).Update("quota_bytes", 8192).Error; err != nil {
		t.Fatal(err)
	}
	rr = f.do(http.MethodPut, path, f.session, `{"from":"other@example.test"}`)
	if rr.Code != 200 {
		t.Fatalf("move into writable account = %d %s", rr.Code, rr.Body)
	}
	for id, expected := range map[uint]int64{f.mailboxID: 0, other: row.SizeBytes} {
		var mailbox models.Mailbox
		if err := f.db.First(&mailbox, id).Error; err != nil || mailbox.QuotaUsedBytes != expected {
			t.Fatalf("moved quota for %d = %d, want %d: %v", id, mailbox.QuotaUsedBytes, expected, err)
		}
	}
}

func TestDraftAttachmentEmptyFilesAndForeignAttachmentIDs(t *testing.T) {
	f := newDraftUploadFixture(t, 8192)
	row := uploadedAttachment(t, f.upload(t, strings.NewReader(""), "empty.pdf"))
	if row.SizeBytes != 0 {
		t.Fatalf("empty upload size = %d", row.SizeBytes)
	}
	other := f.draft
	other.ID = 0
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	rr := f.do(http.MethodDelete, fmt.Sprintf("/api/v1/messages/draft/%d/attachments/%d", other.ID, row.ID), f.session, "")
	if rr.Code != 404 {
		t.Fatalf("attachment outside named draft = %d %s", rr.Code, rr.Body)
	}
	rr = f.do(http.MethodPost, fmt.Sprintf("/api/v1/messages/draft/%d/send", f.draft.ID), f.session, "")
	if rr.Code != 201 {
		t.Fatalf("send empty attachment = %d %s", rr.Code, rr.Body)
	}
}

func TestDraftAttachmentCountRespectsExistingMIMEPartBound(t *testing.T) {
	f := newDraftUploadFixture(t, 1<<20)
	row := uploadedAttachment(t, f.upload(t, strings.NewReader(""), "empty.pdf"))
	var stored models.Attachment
	if err := f.db.First(&stored, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	rows := make([]models.Attachment, rmime.CurrentLimits().MaxParts-3)
	for index := range rows {
		rows[index] = stored
		rows[index].ID = 0
	}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	uploadedAttachment(t, f.upload(t, strings.NewReader(""), "at-limit.pdf"))
	rr := f.upload(t, strings.NewReader(""), "over-limit.pdf")
	if rr.Code != 413 {
		t.Fatalf("part-count limit = %d %s", rr.Code, rr.Body)
	}
}
