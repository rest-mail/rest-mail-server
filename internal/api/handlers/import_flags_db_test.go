package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestImportedDeletedVisibilityClearingAndPermanentRemoval(t *testing.T) {
	database := importProofDB(t)
	fixture := seedAccountsFixture(t, database)
	if err := database.Create(&models.QuotaUsage{MailboxID: fixture.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	importer := NewMessageImportHandler(database, 4096, nil, nil)
	body := apitypes.ImportMessageBody{RawMessage: []byte("Message-ID: <deleted-flags@example.test>\r\nSubject: flags\r\n\r\nbody\r\n"), Folder: "Archive", Flags: []string{`\Deleted`, "$Forwarded", "$Junk"}, InternalDate: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", fixture.user.ID), fixture.admin.ID, nil, string(encoded))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	importMessageRoutes(importer).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	var imported endpoint.Data[apitypes.ImportMessageResult]
	if err := json.Unmarshal(response.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	routes := messageRoutes(NewMessageHandler(database, nil, nil, "", nil))
	call := func(method, url, payload string) *httptest.ResponseRecorder {
		request := authzRequest(method, url, fixture.user.ID, nil, payload)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, request)
		return response
	}
	listing := fmt.Sprintf("/api/v1/accounts/%d/folders/Archive/messages", fixture.user.ID)
	checkCount := func(query string, want int) {
		t.Helper()
		response := call(http.MethodGet, listing+query, "")
		var page endpoint.List[Message]
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != 200 || len(page.Data) != want {
			t.Fatalf("list %s: %d %s (%v)", query, response.Code, response.Body, err)
		}
	}
	checkCount("", 0)
	checkCount("?include_imap_deleted=true", 1)
	target := fmt.Sprintf("/api/v1/messages/%d", imported.Data.ID)
	for _, payload := range []string{`{"imap_deleted":false,"keywords":["$Forwarded","Work"]}`, `{"imap_deleted":true,"keywords":[]}`} {
		response := call(http.MethodPatch, target, payload)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body)
		}
		if bytes.Contains([]byte(payload), []byte("false")) {
			checkCount("", 1)
		} else {
			checkCount("", 0)
		}
	}
	var mailbox models.Mailbox
	if err := database.First(&mailbox, fixture.user.ID).Error; err != nil || mailbox.QuotaUsedBytes == 0 {
		t.Fatal("hidden import must still count toward quota", err)
	}
	response = call(http.MethodDelete, target+"?permanent=true", "")
	if response.Code != http.StatusNoContent {
		t.Fatal(response.Code, response.Body)
	}
	var count int64
	if err := database.Model(&models.Message{}).Where("id = ?", imported.Data.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("EXPUNGE path left a stored message", err)
	}
	if err := database.First(&mailbox, fixture.user.ID).Error; err != nil || mailbox.QuotaUsedBytes != 0 {
		t.Fatalf("EXPUNGE did not reclaim storage: %d %v", mailbox.QuotaUsedBytes, err)
	}
}
