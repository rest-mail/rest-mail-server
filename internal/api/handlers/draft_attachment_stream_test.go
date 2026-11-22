package handlers

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

type boundedDraftUploadReader struct {
	t *testing.T
}

func (source boundedDraftUploadReader) Read(destination []byte) (int, error) {
	if len(destination) > 32<<10 {
		source.t.Fatalf("upload requested a %d-byte memory buffer", len(destination))
	}
	for index := range destination {
		destination[index] = 'x'
	}
	return len(destination), nil
}

func TestDraftAttachmentFilesystemStreamsBoundedChunks(t *testing.T) {
	db := draftSendPostgres(t)
	domain := models.Domain{Name: "upload-filesystem.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "sender", Address: "sender@" + domain.Name, Active: true}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	draft := models.Message{MailboxID: mailbox.ID, Folder: "Drafts", IsDraft: true, PendingDraft: true}
	if err := db.Create(&draft).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewMessageHandler(db, nil, nil, "", nil)
	handler.forwardRoot = t.TempDir()
	const size = 2 << 20
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/messages/draft/%d/attachments", draft.ID), io.LimitReader(boundedDraftUploadReader{t: t}, size))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Attachment-Filename", "large.bin")
	request.Header.Set("X-Attachment-Content-Type", "application/octet-stream")
	request = withClaims(request, &auth.Claims{AccountID: mailbox.ID})
	response := httptest.NewRecorder()
	composeRoutes(handler).ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatalf("filesystem upload = %d %s", response.Code, response.Body)
	}
	var row models.Attachment
	if err := db.Where("message_id = ?", draft.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(row.StorageRef)
	if err != nil || len(data) != size || !bytes.Equal(data, bytes.Repeat([]byte("x"), size)) || row.StorageType != "filesystem" {
		t.Fatalf("filesystem upload changed content: %d bytes, %v", len(data), err)
	}
}

func TestDraftAttachmentUploadRouteOnlyExemptsRawDraftUploads(t *testing.T) {
	for _, path := range []string{"/api/v1/messages/draft/1/attachments", "/api/v1/messages/draft/1/attachments/"} {
		if !DraftAttachmentUploadRoute(httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Errorf("upload path not recognized: %s", path)
		}
		for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
			if DraftAttachmentUploadRoute(httptest.NewRequest(method, path, nil)) {
				t.Errorf("exempted non-upload method: %s %s", method, path)
			}
		}
	}
	for _, path := range []string{"/api/v1/messages/draft/1/attachments/2", "/api/v1/messages/draft/attachments", "/api/v1/messages/draft/1/send", "/api/v1/messages/draft/1/attachments/elsewhere"} {
		if DraftAttachmentUploadRoute(httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Errorf("exempted non-upload path: %s", path)
		}
	}
}
