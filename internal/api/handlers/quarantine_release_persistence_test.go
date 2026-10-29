package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline/filters"
	"gorm.io/gorm"
)

func quarantineReleaseFixture(t *testing.T) (*gorm.DB, models.Mailbox, models.Quarantine) {
	t.Helper()
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(append(accountModels, &models.Message{}, &models.Quarantine{})...); err != nil {
		t.Fatal(err)
	}
	ensureDefaultServerCeiling(t, db)
	domain := models.Domain{Name: "quarantine-release.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "user", Address: "user@quarantine-release.test", QuotaUsedBytes: 13}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	held := models.Quarantine{MailboxID: mailbox.ID, Sender: "sender@example.test", Subject: "Held message",
		BodyPreview: "preview", RawMessage: "Subject: Held message\r\n\r\nhéllo", QuarantineReason: "spam"}
	if err := db.Create(&held).Error; err != nil {
		t.Fatal(err)
	}
	return db, mailbox, held
}

func requestQuarantineRelease(db *gorm.DB, mailbox models.Mailbox, held models.Quarantine, ctx context.Context) *httptest.ResponseRecorder {
	claims := &auth.Claims{AccountID: mailbox.ID, CredentialKind: "app_password"}
	r := httptest.NewRequest(http.MethodPost, quarantinePathOf(mailbox.ID, held.ID, "/release"), nil)
	r = r.WithContext(context.WithValue(ctx, middleware.ClaimsKey, claims))
	rr := httptest.NewRecorder()
	quarantineRoutes(NewPipelineHandler(db, nil, filters.JSSidecar{})).ServeHTTP(rr, r)
	return rr
}

func assertQuarantineReleaseState(t *testing.T, db *gorm.DB, mailbox models.Mailbox, held models.Quarantine, released bool) {
	t.Helper()
	var actual models.Quarantine
	if err := db.First(&actual, held.ID).Error; err != nil {
		t.Fatal(err)
	}
	if actual.Released != released || actual.RawMessage != held.RawMessage {
		t.Fatalf("quarantine state after release: %+v", actual)
	}
	var messages []models.Message
	if err := db.Where("mailbox_id = ?", mailbox.ID).Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	wantCount, wantQuota := 0, mailbox.QuotaUsedBytes
	if released {
		wantCount = 1
		wantQuota += int64(len(held.RawMessage))
	}
	if len(messages) != wantCount {
		t.Fatalf("inbox contains %d messages, want %d", len(messages), wantCount)
	}
	if released {
		msg := messages[0]
		if msg.Folder != "INBOX" || msg.Sender != held.Sender || msg.Subject != held.Subject || msg.RawMessage != held.RawMessage ||
			msg.BodyText != "héllo" || msg.BodyHTML != "" || msg.SizeBytes != len(held.RawMessage) || msg.RawSize != len(held.RawMessage) {
			t.Fatalf("released message lost content or raw octet count: %+v", msg)
		}
	}
	var current models.Mailbox
	if err := db.First(&current, mailbox.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.QuotaUsedBytes != wantQuota {
		t.Fatalf("quota %d, want %d", current.QuotaUsedBytes, wantQuota)
	}
}

// Each failure happens against real PostgreSQL state. Removing the fault must
// leave the same held message retryable, with one inbox copy and one quota charge.
func TestQuarantineReleaseRollsBackEveryStorageFailure(t *testing.T) {
	for _, point := range []string{"lookup", "released flag", "inbox insert", "quota update", "commit", "cancel after insert"} {
		t.Run(point, func(t *testing.T) {
			db, mailbox, held := quarantineReleaseFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := func(tx *gorm.DB) { _ = tx.AddError(errors.New("private quarantine database detail")) }
			var remove func() error
			const callback = "quarantine_release_failure"
			switch point {
			case "lookup":
				if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "quarantine" {
						failure(tx)
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Callback().Query().Remove(callback) }
			case "released flag", "quota update":
				table := "quarantine"
				if point == "quota update" {
					table = "mailboxes"
				}
				if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == table {
						failure(tx)
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Callback().Update().Remove(callback) }
			case "inbox insert", "cancel after insert":
				position := db.Callback().Create().Before("gorm:create")
				if point == "cancel after insert" {
					position = db.Callback().Create().After("gorm:create")
				}
				if err := position.Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "messages" {
						if point == "cancel after insert" {
							cancel()
						} else {
							failure(tx)
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Callback().Create().Remove(callback) }
			case "commit":
				// A deferred constraint trigger fails only at the real transaction
				// commit, after every statement has otherwise succeeded.
				if err := db.Exec(`CREATE FUNCTION fail_quarantine_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'private quarantine commit detail'; END $$`).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(`CREATE CONSTRAINT TRIGGER quarantine_commit_failure AFTER UPDATE ON quarantine
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_quarantine_commit()`).Error; err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Exec("DROP TRIGGER quarantine_commit_failure ON quarantine").Error }
			}
			t.Cleanup(func() {
				if remove != nil {
					_ = remove()
				}
			})
			rr := requestQuarantineRelease(db, mailbox, held, ctx)
			if err := remove(); err != nil {
				t.Fatal(err)
			}
			remove = nil
			if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), `"internal_error"`) || strings.Contains(rr.Body.String(), "private") {
				t.Errorf("storage failure must be a sanitized 500: %d %s", rr.Code, rr.Body)
			}
			assertQuarantineReleaseState(t, db, mailbox, held, false)
			rr = requestQuarantineRelease(db, mailbox, held, t.Context())
			if rr.Code != http.StatusOK {
				t.Fatalf("retry after removing fault: %d %s", rr.Code, rr.Body)
			}
			assertQuarantineReleaseState(t, db, mailbox, held, true)
			var answer struct{ Data QuarantineReleased }
			if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
				t.Fatal(err)
			}
			id, err := strconv.ParseUint(answer.Data.MessageID, 10, 64)
			var msg models.Message
			if err != nil || id == 0 || answer.Data.Status != "released" || db.First(&msg, id).Error != nil || msg.MailboxID != mailbox.ID {
				t.Fatalf("release response does not name the committed inbox message: %+v", answer)
			}
		})
	}
}
