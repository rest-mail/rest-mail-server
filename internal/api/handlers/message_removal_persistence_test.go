package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func TestMessageRemovalPersistenceQuotaAndEvents(t *testing.T) {
	for _, scenario := range []string{"trash", "permanent", "already_trash", "already_deleted", "move_failure", "sum_failure", "attachment_delete_failure", "message_delete_failure", "quota_failure", "cancelled", "concurrent"} {
		t.Run(scenario, func(t *testing.T) {
			db := openCustomFilterCreateScratchDB(t)
			if err := db.AutoMigrate(&models.Mailbox{}, &models.Message{}, &models.Attachment{}, &models.Role{}, &models.Capability{}, &models.RoleCapability{}, &models.Grant{}, &models.GrantActivation{}, &models.Credential{}); err != nil {
				t.Fatal(err)
			}
			create := func(v interface{}) {
				t.Helper()
				if err := db.Create(v).Error; err != nil {
					t.Fatal(err)
				}
			}
			d := models.Domain{Name: "example.org"}
			create(&d)
			owner := models.Mailbox{DomainID: d.ID, LocalPart: "owner", Address: "owner@example.org", Active: true, QuotaUsedBytes: 680}
			other := models.Mailbox{DomainID: d.ID, LocalPart: "other", Address: "other@example.org", Active: true, QuotaUsedBytes: 700}
			create(&owner)
			create(&other)
			msg := models.Message{MailboxID: owner.ID, Folder: "INBOX", SizeBytes: 80, HasAttachments: true}
			if scenario == "already_trash" {
				msg.Folder = "Trash"
			}
			if scenario == "already_deleted" {
				msg.IsDeleted = true
				if err := db.Model(&owner).Update("quota_used_bytes", 500).Error; err != nil {
					t.Fatal(err)
				}
			}
			create(&msg)
			att := models.Attachment{MessageID: msg.ID, SizeBytes: 100, Filename: "Public fixture", StorageRef: "unused-public-fixture"}
			create(&att)
			sibling := models.Message{MailboxID: owner.ID, Folder: "Archive", SizeBytes: 500}
			foreign := models.Message{MailboxID: other.ID, Folder: "INBOX", SizeBytes: 700}
			create(&sibling)
			create(&foreign)
			broker := NewSSEBroker()
			events := broker.Subscribe(owner.ID)
			foreignEvents := broker.Subscribe(other.ID)
			t.Cleanup(func() { broker.Unsubscribe(owner.ID, events); broker.Unsubscribe(other.ID, foreignEvents) })
			h := NewMessageHandler(db, broker, nil, "", nil)
			path := fmt.Sprintf("/api/v1/messages/%d", msg.ID)
			hard := scenario != "trash" && scenario != "move_failure" && scenario != "cancelled"
			if hard && scenario != "already_trash" && scenario != "already_deleted" {
				path += "?permanent=true"
			}
			r := withClaims(httptest.NewRequest(http.MethodDelete, path, nil), &auth.Claims{AccountID: owner.ID, Address: owner.Address, AMR: []string{auth.AMRPassword}})
			if scenario == "cancelled" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			if scenario == "move_failure" || scenario == "quota_failure" {
				if err := db.Callback().Update().Before("gorm:update").Register("message-removal:update", func(tx *gorm.DB) {
					if scenario == "move_failure" && tx.Statement.Table == "messages" || scenario == "quota_failure" && tx.Statement.Table == "mailboxes" {
						tx.AddError(errors.New("private message update failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "sum_failure" {
				if err := db.Callback().Row().Before("gorm:row").Register("message-removal:sum", func(tx *gorm.DB) {
					if tx.Statement.Table == "attachments" {
						tx.AddError(errors.New("private attachment sum failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "attachment_delete_failure" || scenario == "message_delete_failure" {
				if err := db.Callback().Delete().Before("gorm:delete").Register("message-removal:delete", func(tx *gorm.DB) {
					if scenario == "attachment_delete_failure" && tx.Statement.Table == "attachments" || scenario == "message_delete_failure" && tx.Statement.Table == "messages" {
						tx.AddError(errors.New("private purge failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			concurrentDone := false
			if scenario == "concurrent" {
				if err := db.Callback().Query().After("gorm:after_query").Register("message-removal:concurrent", func(tx *gorm.DB) {
					if concurrentDone || tx.Statement.Table != "messages" {
						return
					}
					concurrentDone = true
					// Simulate a purge committed after ownership lookup, before this request's
					// delete transaction. No persistent database outside this scratch DB is used.
					for _, q := range []string{"DELETE FROM attachments WHERE message_id = ?", "DELETE FROM messages WHERE id = ?"} {
						if err := tx.Session(&gorm.Session{NewDB: true}).Exec(q, msg.ID).Error; err != nil {
							tx.AddError(err)
							return
						}
					}
					if err := tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE mailboxes SET quota_used_bytes = 500 WHERE id = ?", owner.ID).Error; err != nil {
						tx.AddError(err)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			rr := httptest.NewRecorder()
			messageRoutes(h).ServeHTTP(rr, r)
			if scenario == "move_failure" || scenario == "quota_failure" {
				if err := db.Callback().Update().Remove("message-removal:update"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "sum_failure" {
				if err := db.Callback().Row().Remove("message-removal:sum"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "attachment_delete_failure" || scenario == "message_delete_failure" {
				if err := db.Callback().Delete().Remove("message-removal:delete"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "concurrent" {
				if err := db.Callback().Query().Remove("message-removal:concurrent"); err != nil {
					t.Fatal(err)
				}
				if !concurrentDone {
					t.Fatal("stale purge fixture did not run")
				}
			}
			success := scenario == "trash" || scenario == "permanent" || scenario == "already_trash" || scenario == "already_deleted" || scenario == "concurrent"
			want := 500
			if success {
				want = 204
			}
			if rr.Code != want || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("response:%d %s want%d", rr.Code, rr.Body, want)
			}
			if success && (rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "") {
				t.Fatal("204 returned a representation")
			}
			var stored models.Mailbox
			if err := db.First(&stored, owner.ID).Error; err != nil {
				t.Fatal(err)
			}
			wantQuota := int64(680)
			purged := success && hard
			if purged {
				wantQuota = 500
			}
			if scenario == "already_deleted" {
				wantQuota = 500
			}
			if stored.QuotaUsedBytes != wantQuota {
				t.Fatalf("quota:%d want%d", stored.QuotaUsedBytes, wantQuota)
			}
			count := func(model interface{}, id uint) int64 {
				t.Helper()
				var n int64
				if err := db.Model(model).Where("id = ?", id).Count(&n).Error; err != nil {
					t.Fatal(err)
				}
				return n
			}
			if (count(&models.Message{}, msg.ID) == 0) != purged || (count(&models.Attachment{}, att.ID) == 0) != purged {
				t.Fatal("message/attachment persistence or rollback")
			}
			if !purged {
				var row models.Message
				if err := db.First(&row, msg.ID).Error; err != nil {
					t.Fatal(err)
				}
				wantFolder := "INBOX"
				if scenario == "trash" {
					wantFolder = "Trash"
				}
				if row.Folder != wantFolder || row.IsDeleted {
					t.Fatal("failed move or visible Trash state")
				}
			}
			if count(&models.Message{}, sibling.ID) != 1 || count(&models.Message{}, foreign.ID) != 1 {
				t.Fatal("another message removed")
			}
			stored = models.Mailbox{}
			if err := db.First(&stored, other.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.QuotaUsedBytes != 700 {
				t.Fatal("another account quota changed")
			}
			select {
			case event := <-events:
				if !success || event.Event.Type != "message_deleted" || event.Event.Data.(map[string]interface{})["message_id"] != msg.ID {
					t.Fatal("deletion event escaped success/ownership")
				}
			default:
				if success {
					t.Fatal("successful deletion event missing")
				}
			}
			select {
			case <-events:
				t.Fatal("duplicate event")
			default:
			}
			select {
			case <-foreignEvents:
				t.Fatal("deletion event sent to another account")
			default:
			}
		})
	}
}
