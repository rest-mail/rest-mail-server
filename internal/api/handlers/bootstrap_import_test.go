package handlers

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestBootstrapAdminCanImportOnFreshInstall(t *testing.T) {
	database := openScratchDB(t, "bootstrapimport", []any{withFullSchema, withRoles})
	if outcome, err := bootstrap.EnsureFirstAdmin(database, "admin@bootstrap.test", "correct-horse-battery-staple", true, time.Now()); err != nil || outcome != bootstrap.FirstAdminCreated {
		t.Fatalf("bootstrap outcome=%v error=%v", outcome, err)
	}
	var admin models.Mailbox
	if err := database.Where("address = ?", "admin@bootstrap.test").First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	const raw = "Message-ID: <bootstrap-import@old.test>\r\nFrom: old@old.test\r\nSubject: imported\r\n\r\nhistorical mail\r\n"
	body := fmt.Sprintf(`{"raw_message":%q,"folder":"INBOX","internal_date":"2020-01-02T03:04:05Z"}`, base64.StdEncoding.EncodeToString([]byte(raw)))
	request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", admin.ID), admin.ID, nil, body)
	request.Header.Set("Content-Type", "application/json")
	handler := NewMessageImportHandler(database, 4096, nil, nil)
	handler.directory = t.TempDir()
	response := httptest.NewRecorder()
	importMessageRoutes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("bootstrap admin import: want 200, got %d %s", response.Code, response.Body)
	}
	var usage models.QuotaUsage
	if err := database.Where("mailbox_id = ?", admin.ID).First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	var message models.Message
	if err := database.Where("mailbox_id = ?", admin.ID).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	wantBytes := int64(len(message.Subject) + len(message.BodyText) + len(message.BodyHTML))
	if usage.MessageCount != 1 || usage.TotalBytes() != wantBytes || wantBytes == 0 {
		t.Fatalf("quota after import: %+v, want one message and %d bytes", usage, wantBytes)
	}
}
