package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func sealSourceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	config := testDBConfig()
	dsn := url.URL{Scheme: "postgresql", User: url.UserPassword(config.DBUser, config.DBPass), Host: net.JoinHostPort(config.DBHost, strconv.Itoa(config.DBPort)), Path: config.DBName, RawQuery: "sslmode=disable"}
	database, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn.String(), PreferSimpleProtocol: true}), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := migrateModels(database, append(accountModels, &models.Message{}, &models.Attachment{}, &models.AuditEntry{}, &models.Pipeline{}, &models.Alias{})...); err != nil {
		t.Fatal(err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { transaction.Rollback() })
	return transaction
}

func assertStoredSeal(t *testing.T, database *gorm.DB, id uint) models.Message {
	t.Helper()
	var message models.Message
	if err := database.First(&message, id).Error; err != nil {
		t.Fatal(err)
	}
	digest, err := message.SealDigest()
	if err != nil || message.SealedHash == "" || digest != message.SealedHash {
		t.Fatalf("receipt not sealed: stored %q, computed %q, error %v", message.SealedHash, digest, err)
	}
	return message
}

func TestMessageSealAppendAndStructuredDelivery(t *testing.T) {
	database := sealSourceTestDB(t)
	fixture := seedMsgDeleteFixture(t, database, 0)
	handler := NewMessageHandler(database, nil, nil, "", nil)
	message, err := handler.deliverToLocal(context.Background(), localDeliveryParams{
		Mailbox: fixture.mailbox, Sender: "sender@example.test", Subject: "Structured", BodyText: "body",
	})
	if err != nil || message == nil {
		t.Fatalf("structured delivery: %v, %v", message, err)
	}
	assertStoredSeal(t, database, message.ID)
	if err := database.Create(&models.QuotaUsage{MailboxID: fixture.mailbox.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(database, &fixture.mailbox); err != nil {
		t.Fatal(err)
	}
	importer := NewMessageImportHandler(database, 1<<20, nil, nil)
	importer.directory = t.TempDir()
	request := httptest.NewRequest(http.MethodPost, "/import", nil)
	request = request.WithContext(context.WithValue(request.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fixture.mailbox.ID}))
	input := importMessageRequest{ID: uint32(fixture.mailbox.ID), Body: apitypes.ImportMessageBody{
		RawMessage: []byte("From: sender@example.test\r\nSubject: APPEND\r\n\r\nbody"), Folder: "Drafts", InternalDate: time.Now(), Flags: []string{`\Draft`},
	}}
	appended, err := importer.importMessage(request, input)
	if err != nil || !appended.Imported {
		t.Fatalf("APPEND import: %+v, %v", appended, err)
	}
	assertStoredSeal(t, database, appended.ID)
	if err := database.Model(&models.Message{}).Where("id = ?", appended.ID).UpdateColumn("sealed_hash", nil).Error; err != nil {
		t.Fatal(err)
	}
	input.Body.Folder = "Archive"
	copied, err := importer.importMessage(request, input)
	if err != nil || !copied.Imported || copied.ID == appended.ID {
		t.Fatalf("COPY import: %+v, %v", copied, err)
	}
	assertStoredSeal(t, database, copied.ID)
	var source models.Message
	if err := database.First(&source, appended.ID).Error; err != nil || source.SealedHash != "" {
		t.Fatalf("COPY backfilled its unsealed source: %+v, %v", source, err)
	}
}

func TestMessageSealImportPreservesBinaryAndDoesNotBackfillDuplicate(t *testing.T) {
	database := sealSourceTestDB(t)
	fixture := seedMsgDeleteFixture(t, database, 0)
	if err := database.Create(&models.QuotaUsage{MailboxID: fixture.mailbox.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(database, &fixture.mailbox); err != nil {
		t.Fatal(err)
	}
	handler := NewMessageImportHandler(database, 1<<20, nil, nil)
	handler.directory = t.TempDir()
	for _, body := range []string{"body", "binary\x00\xffbody"} {
		raw := []byte("From: sender@example.test\r\nSubject: Import\r\n\r\n" + body)
		input := importMessageRequest{ID: uint32(fixture.mailbox.ID), Body: apitypes.ImportMessageBody{
			RawMessage: raw, Folder: "Archive", InternalDate: time.Now(), Flags: []string{`\Draft`},
		}}
		request := httptest.NewRequest(http.MethodPost, "/import", nil)
		request = request.WithContext(context.WithValue(request.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fixture.mailbox.ID}))
		result, err := handler.importMessage(request, input)
		if err != nil || !result.Imported {
			t.Fatalf("import: %+v, %v", result, err)
		}
		message := assertStoredSeal(t, database, result.ID)
		if string(message.Source()) != string(raw) {
			t.Fatal("import changed source octets")
		}
		if err := database.Model(&message).UpdateColumn("sealed_hash", nil).Error; err != nil {
			t.Fatal(err)
		}
		result, err = handler.importMessage(request, input)
		if err != nil || result.Imported {
			t.Fatalf("duplicate import: %+v, %v", result, err)
		}
		if err := database.First(&message, message.ID).Error; err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := database.Model(&models.Message{}).Where("id = ? AND sealed_hash IS NULL", message.ID).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("duplicate backfilled old message: count %d, error %v", count, err)
		}
	}
}

func TestMessageSealRESTMAILAcceptance(t *testing.T) {
	database := sealSourceTestDB(t)
	fixture := seedMsgDeleteFixture(t, database, 0)
	// Mail is delivered only to a live domain's mailboxes (#1845).
	if err := database.Model(&models.Domain{}).Where("id = ?", fixture.mailbox.DomainID).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewRestmailHandler(database, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{})
	delivered, failed := deliverRESTMAIL(t, handler, "sender@example.test", "From: sender@example.test\r\nSubject: RESTMAIL\r\n\r\nbody", fixture.mailbox.Address)
	if len(delivered) != 1 || len(failed) != 0 {
		t.Fatalf("RESTMAIL delivery: %v, failed %v", delivered, failed)
	}
	var message models.Message
	if err := database.Where("mailbox_id = ?", fixture.mailbox.ID).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	assertStoredSeal(t, database, message.ID)
}

func TestMessageSealMigrationKeepsHistoricalRowsUnsealed(t *testing.T) {
	database := sealSourceTestDB(t)
	fixture := seedMsgDeleteFixture(t, database, 0)
	schema := fmt.Sprintf("seal_migration_%d", time.Now().UnixNano())
	for _, query := range []string{
		"CREATE SCHEMA " + schema,
		"CREATE TABLE " + schema + ".domains (LIKE public.domains INCLUDING ALL)",
		"CREATE TABLE " + schema + ".mailboxes (LIKE public.mailboxes INCLUDING ALL)",
		fmt.Sprintf("INSERT INTO %s.domains SELECT * FROM public.domains WHERE id = %d", schema, fixture.mailbox.DomainID),
		fmt.Sprintf("INSERT INTO %s.mailboxes SELECT * FROM public.mailboxes WHERE id = %d", schema, fixture.mailbox.ID),
		"CREATE TABLE " + schema + ".messages (LIKE public.messages INCLUDING ALL)",
		"ALTER TABLE " + schema + ".messages DROP COLUMN sealed_hash",
		"SET LOCAL search_path TO " + schema + ", public",
	} {
		if err := database.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Exec("INSERT INTO messages (mailbox_id, sender, raw_message, body_text) VALUES (?, ?, ?, ?)", fixture.mailbox.ID, "old@example.test", "Subject: Old\r\n\r\nbody", "body").Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateModels(database, &models.Message{}); err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := database.Model(&models.Message{}).Where("sealed_hash IS NULL").Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("migration backfilled seal: count %d, error %v", count, err)
		}
	}
	message := models.Message{MailboxID: fixture.mailbox.ID, Sender: "new@example.test", BodyText: "new body"}
	if err := database.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	assertStoredSeal(t, database, message.ID)
}
