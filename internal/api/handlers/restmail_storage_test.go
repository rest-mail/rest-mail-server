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
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

type restmailStorageContextKey struct{}

func restmailStorageFixture(t *testing.T) (*gorm.DB, models.Mailbox) {
	t.Helper()
	db := openCustomFilterCreateScratchDB(t)
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Message{}, &models.QuotaUsage{}, &models.Pipeline{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "restmail-storage.test"}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "user", Address: "user@restmail-storage.test", QuotaUsedBytes: 13}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	usage := models.QuotaUsage{MailboxID: mailbox.ID, SubjectBytes: 4, BodyBytes: 9, MessageCount: 2}
	if err := db.Create(&usage).Error; err != nil {
		t.Fatal(err)
	}
	return db, mailbox
}

func restmailStorageRequest(t *testing.T, db *gorm.DB, ctx context.Context, to []string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"from": "sender@external.test", "to": to, "subject": "storage", "body_text": "héllo", "body_html": "<p>héllo</p>",
		"message_id": "<storage@external.test>", "raw_message": "From: sender@external.test\r\nSubject: storage\r\n\r\nhéllo",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, restmailMessagesPath, strings.NewReader(string(body))).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.0.2.10:1234"
	h := NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}})
	rr := httptest.NewRecorder()
	restmailRoutes(h).ServeHTTP(rr, r)
	return rr
}

func assertRESTMAILStorage(t *testing.T, db *gorm.DB, mailbox models.Mailbox, stored bool) {
	t.Helper()
	var messages []models.Message
	if err := db.Where("mailbox_id = ?", mailbox.ID).Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	wantCount, wantQuota, wantSubject, wantBody, wantMessages := 0, int64(13), int64(4), int64(9), 2
	if stored {
		wantCount = 1
		wantSubject += int64(len("storage"))
		wantBody += int64(len("héllo") + len("<p>héllo</p>"))
		wantQuota += int64(len("storage") + len("héllo") + len("<p>héllo</p>"))
		wantMessages++
	}
	if len(messages) != wantCount {
		t.Errorf("stored messages = %d, want %d", len(messages), wantCount)
	}
	if stored && len(messages) == 1 {
		m := messages[0]
		if m.Folder != "INBOX" || m.MsgID != "<storage@external.test>" || m.Subject != "storage" || m.BodyText != "héllo" || m.BodyHTML != "<p>héllo</p>" ||
			m.SizeBytes != len("storage")+len("héllo")+len("<p>héllo</p>") || m.RawSize != len(m.RawMessage) || !strings.Contains(m.RawMessage, "\r\n\r\nhéllo") {
			t.Errorf("stored message lost content or byte counts: %+v", m)
		}
	}
	var current models.Mailbox
	if err := db.First(&current, mailbox.ID).Error; err != nil {
		t.Fatal(err)
	}
	var usage models.QuotaUsage
	if err := db.First(&usage, "mailbox_id = ?", mailbox.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.QuotaUsedBytes != wantQuota || usage.SubjectBytes != wantSubject || usage.BodyBytes != wantBody || usage.MessageCount != wantMessages || usage.AttachmentBytes != 0 {
		t.Errorf("quota = %d, %+v; want total=%d subject=%d body=%d count=%d", current.QuotaUsedBytes, usage, wantQuota, wantSubject, wantBody, wantMessages)
	}
}

// Every storage failure must undo this recipient's insert and both quota
// charges. The existing all-failed response remains 422; a clean retry stores
// one message and charges it exactly once. Only private scratch databases are used.
func TestRESTMAILRecipientStorageRollsBackAndRetries(t *testing.T) {
	for _, point := range []string{"message insert", "mailbox quota", "quota breakdown", "commit", "cancel after insert", "cancel after quota"} {
		t.Run(point, func(t *testing.T) {
			db, mailbox := restmailStorageFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var remove func() error
			const callback = "restmail_storage_failure"
			fail := func(tx *gorm.DB) { _ = tx.AddError(errors.New("private storage dependency detail")) }
			switch point {
			case "message insert", "cancel after insert":
				position := db.Callback().Create().Before("gorm:create")
				if point == "cancel after insert" {
					position = db.Callback().Create().After("gorm:create")
				}
				if err := position.Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "messages" {
						if point == "cancel after insert" {
							cancel()
						} else {
							fail(tx)
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Callback().Create().Remove(callback) }
			case "mailbox quota", "quota breakdown", "cancel after quota":
				table := "mailboxes"
				position := db.Callback().Update().Before("gorm:update")
				if point == "quota breakdown" {
					table = "quota_usage"
				}
				if point == "cancel after quota" {
					position = db.Callback().Update().After("gorm:update")
				}
				if err := position.Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == table {
						if point == "cancel after quota" {
							cancel()
						} else {
							fail(tx)
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Callback().Update().Remove(callback) }
			case "commit":
				// PostgreSQL rejects the real commit after all writes succeeded.
				if err := db.Exec(`CREATE FUNCTION fail_restmail_storage_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'private storage commit detail'; END $$`).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(`CREATE CONSTRAINT TRIGGER restmail_storage_commit_failure AFTER UPDATE ON quota_usage
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_restmail_storage_commit()`).Error; err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Exec("DROP TRIGGER restmail_storage_commit_failure ON quota_usage").Error }
			}
			t.Cleanup(func() {
				if remove != nil {
					_ = remove()
				}
			})
			rr := restmailStorageRequest(t, db, ctx, []string{mailbox.Address})
			if err := remove(); err != nil {
				t.Fatal(err)
			}
			remove = nil
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"undeliverable"`) || strings.Contains(rr.Body.String(), "private") {
				t.Errorf("failed recipient must not be acknowledged: %d %s", rr.Code, rr.Body)
			}
			assertRESTMAILStorage(t, db, mailbox, false)
			rr = restmailStorageRequest(t, db, t.Context(), []string{mailbox.Address})
			if rr.Code != http.StatusCreated {
				t.Fatalf("clean retry: %d %s", rr.Code, rr.Body)
			}
			assertRESTMAILStorage(t, db, mailbox, true)
		})
	}
}

func TestRESTMAILRecipientStorageRetainsRequestContext(t *testing.T) {
	db, mailbox := restmailStorageFixture(t)
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), restmailStorageContextKey{}, true), 5*time.Second)
	defer cancel()
	seen := map[string]int{}
	check := func(tx *gorm.DB) {
		seen[tx.Statement.Table]++
		deadline, ok := tx.Statement.Context.Deadline()
		want, _ := ctx.Deadline()
		if !ok || !deadline.Equal(want) || tx.Statement.Context.Value(restmailStorageContextKey{}) != true {
			t.Errorf("%s lost HTTP context/deadline", tx.Statement.Table)
		}
	}
	const callback = "restmail_storage_context"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, check); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register(callback, check); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Create().Remove(callback)
		_ = db.Callback().Update().Remove(callback)
	})
	rr := restmailStorageRequest(t, db, ctx, []string{mailbox.Address})
	if rr.Code != http.StatusCreated {
		t.Fatalf("successful delivery: %d %s", rr.Code, rr.Body)
	}
	for _, table := range []string{"messages", "mailboxes", "quota_usage"} {
		if seen[table] != 1 {
			t.Errorf("%s writes = %d, want 1", table, seen[table])
		}
	}
	assertRESTMAILStorage(t, db, mailbox, true)
}

func TestRESTMAILRecipientStoragePreservesPartialDelivery(t *testing.T) {
	db, mailbox := restmailStorageFixture(t)
	other := models.Mailbox{DomainID: mailbox.DomainID, LocalPart: "other", Address: "other@restmail-storage.test", QuotaUsedBytes: 13}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.QuotaUsage{MailboxID: other.ID, SubjectBytes: 4, BodyBytes: 9, MessageCount: 2}).Error; err != nil {
		t.Fatal(err)
	}
	// Reject only the first recipient's quota write, after its insert. The
	// second recipient still commits through the existing fan-out policy.
	if err := db.Exec(`CREATE FUNCTION fail_first_restmail_recipient() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.mailbox_id = ` + strconv.FormatUint(uint64(mailbox.ID), 10) + ` THEN RAISE EXCEPTION 'private first recipient detail'; END IF; RETURN NEW; END $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER first_restmail_recipient_failure BEFORE UPDATE ON quota_usage
FOR EACH ROW EXECUTE FUNCTION fail_first_restmail_recipient()`).Error; err != nil {
		t.Fatal(err)
	}
	rr := restmailStorageRequest(t, db, t.Context(), []string{mailbox.Address, other.Address})
	var answer struct{ Data RestmailDelivery }
	if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusCreated || len(answer.Data.Delivered) != 1 || answer.Data.Delivered[0] != other.Address || len(answer.Data.Failed) != 1 || answer.Data.Failed[0] != mailbox.Address || strings.Contains(rr.Body.String(), "private") {
		t.Errorf("partial recipient result: %d %s", rr.Code, rr.Body)
	}
	assertRESTMAILStorage(t, db, mailbox, false)
	assertRESTMAILStorage(t, db, other, true)
}
