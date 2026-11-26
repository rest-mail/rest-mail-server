package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
)

type draftUploadFixture struct {
	*credentialFixture
	draft models.Message
	store *attachmentstore.S3
}

func newDraftUploadFixture(t *testing.T, maxBytes int64) *draftUploadFixture {
	t.Helper()
	previous := sessionCheck
	sessionCheck = cachedSessionCheck
	t.Cleanup(func() { sessionCheck = previous })
	fake := s3test.New(t)
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", Attachments: fake.Config, SMTPMaxMessageSize: maxBytes, APIMaxBodyBytes: 1024})
	draft := models.Message{MailboxID: f.mailboxID, Folder: "Drafts", IsDraft: true, PendingDraft: true,
		Sender: f.address, Subject: "Upload", BodyText: "Review", RecipientsTo: models.JSONB(`["friend@remote.example"]`), RecipientsCc: models.JSONB(`[]`)}
	if err := f.db.Create(&draft).Error; err != nil {
		t.Fatal(err)
	}
	return &draftUploadFixture{credentialFixture: f, draft: draft, store: attachmentstore.NewS3(fake.Config)}
}

func (f *draftUploadFixture) path() string {
	return fmt.Sprintf("/api/v1/messages/draft/%d/attachments", f.draft.ID)
}

func (f *draftUploadFixture) upload(t *testing.T, body io.Reader, filename string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, f.path(), body)
	req.Header.Set("Authorization", f.session)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Attachment-Filename", filename)
	req.Header.Set("X-Attachment-Content-Type", "application/pdf")
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func uploadedAttachment(t *testing.T, rr *httptest.ResponseRecorder) models.Attachment {
	t.Helper()
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s; want 201", rr.Code, rr.Body)
	}
	var answer struct {
		Data models.Attachment `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer.Data
}

func TestDraftAttachmentUploadListDelete(t *testing.T) {
	f := newDraftUploadFixture(t, 1<<20)
	data := bytes.Repeat([]byte("\x00attachment\xff"), 4096)
	row := uploadedAttachment(t, f.upload(t, bytes.NewReader(data), "report.pdf"))
	if row.ID == 0 || row.Filename != "report.pdf" || row.ContentType != "application/pdf" || row.SizeBytes != int64(len(data)) {
		t.Fatalf("incorrect upload metadata: %+v", row)
	}
	var stored models.Attachment
	if err := f.db.First(&stored, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	reader, err := f.store.Open(t.Context(), stored.StorageRef)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(got, data) || stored.Checksum != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatal("store does not hold exact uploaded octets")
	}
	rr := f.do(http.MethodGet, f.path(), f.session, "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"filename":"report.pdf"`) {
		t.Fatalf("list = %d %s", rr.Code, rr.Body)
	}
	rr = f.do(http.MethodDelete, fmt.Sprintf("%s/%d", f.path(), row.ID), f.session, "")
	if rr.Code != 204 {
		t.Fatalf("delete = %d %s", rr.Code, rr.Body)
	}
	rr = f.do(http.MethodGet, f.path(), f.session, "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"data":[]`) {
		t.Fatalf("deleted upload still listed: %d %s", rr.Code, rr.Body)
	}
	var mailbox models.Mailbox
	if err := f.db.First(&mailbox, f.mailboxID).Error; err != nil || mailbox.QuotaUsedBytes != 0 {
		t.Fatalf("deletion did not release quota: %+v %v", mailbox, err)
	}
}

func TestDraftAttachmentSendPersistsMIMEAndQueue(t *testing.T) {
	f := newDraftUploadFixture(t, 1<<20)
	data := []byte("%PDF-1.7\n\x00uploaded\xff\n")
	row := uploadedAttachment(t, f.upload(t, bytes.NewReader(data), "résumé.pdf"))
	rr := f.do(http.MethodPost, fmt.Sprintf("/api/v1/messages/draft/%d/send", f.draft.ID), f.session, "")
	if rr.Code != 201 {
		t.Fatalf("send = %d %s", rr.Code, rr.Body)
	}
	var sent models.Message
	if err := f.db.Where("mailbox_id = ? AND folder = ?", f.mailboxID, "Sent").First(&sent).Error; err != nil {
		t.Fatal(err)
	}
	var queued models.OutboundQueue
	if err := f.db.First(&queued).Error; err != nil || queued.RawMessage != sent.RawMessage {
		t.Fatalf("queue does not hold exact Sent MIME: %v", err)
	}
	for _, raw := range []string{sent.RawMessage, queued.RawMessage} {
		parsed, err := rmime.Parse([]byte(raw))
		if err != nil || len(parsed.Attachments) != 1 {
			t.Fatalf("MIME attachment missing: %v", err)
		}
		part := parsed.Attachments[0]
		decoded, err := base64.StdEncoding.DecodeString(part.Content)
		if err != nil || !bytes.Equal(decoded, data) || part.Filename != row.Filename || part.ContentType != row.ContentType {
			t.Fatalf("MIME attachment changed: %+v %v", part, err)
		}
	}
	var count int64
	if err := f.db.Model(&models.Attachment{}).Where("message_id = ?", f.draft.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("sent draft retained attachment rows")
	}
}

func TestDraftAttachmentLimitsAndOwnership(t *testing.T) {
	t.Run("message_size", func(t *testing.T) {
		f := newDraftUploadFixture(t, 8192)
		rr := f.upload(t, io.LimitReader(zeroUploadReader{}, 8193), "large.pdf")
		if rr.Code != 413 {
			t.Fatalf("oversized stream = %d %s", rr.Code, rr.Body)
		}
	})
	t.Run("quota", func(t *testing.T) {
		f := newDraftUploadFixture(t, 8192)
		if err := f.db.Model(&models.Mailbox{}).Where("id = ?", f.mailboxID).Update("quota_bytes", 3).Error; err != nil {
			t.Fatal(err)
		}
		rr := f.upload(t, strings.NewReader("four"), "full.pdf")
		if rr.Code != 422 || !strings.Contains(rr.Body.String(), "mailbox_full") {
			t.Fatalf("quota = %d %s", rr.Code, rr.Body)
		}
	})
	t.Run("ownership", func(t *testing.T) {
		f := newDraftUploadFixture(t, 8192)
		row := uploadedAttachment(t, f.upload(t, strings.NewReader("private"), "private.pdf"))
		other := f.seedMailbox(1, "stranger")
		pair, err := f.jwt.GenerateSession(other, "stranger@example.test", 0, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		f.session = "Bearer " + pair.AccessToken
		for _, rr := range []*httptest.ResponseRecorder{
			f.upload(t, strings.NewReader("intrusion"), "foreign.pdf"),
			f.do(http.MethodGet, f.path(), f.session, ""),
			f.do(http.MethodDelete, fmt.Sprintf("%s/%d", f.path(), row.ID), f.session, ""),
			f.do(http.MethodPost, fmt.Sprintf("/api/v1/messages/draft/%d/send", f.draft.ID), f.session, ""),
		} {
			if rr.Code != 404 {
				t.Fatalf("foreign draft = %d %s", rr.Code, rr.Body)
			}
		}
	})
}

type zeroUploadReader struct{}

func (zeroUploadReader) Read(destination []byte) (int, error) {
	clear(destination)
	return len(destination), nil
}

func TestDraftAttachmentContractDescription(t *testing.T) {
	routers := describedRouters(t)
	rr := httptest.NewRecorder()
	routers.Public.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/docs/openapi.json", nil))
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for path, methods := range map[string][]string{
		"/api/v1/messages/draft/{id}/attachments":                {"post", "get"},
		"/api/v1/messages/draft/{id}/attachments/{attachmentId}": {"delete"},
	} {
		for _, method := range methods {
			if len(doc.Paths[path][method]) == 0 {
				t.Errorf("missing contract: %s %s", method, path)
			}
		}
	}
	var operation struct {
		Parameters []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(doc.Paths["/api/v1/messages/draft/{id}/attachments"]["post"], &operation); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"X-Attachment-Filename", "X-Attachment-Content-Type"} {
		found := false
		for _, parameter := range operation.Parameters {
			if parameter.Name == name && parameter.Required {
				found = true
			}
		}
		if !found {
			t.Errorf("upload contract does not require %s", name)
		}
	}
}

func TestDraftAttachmentMetadataValidation(t *testing.T) {
	f := newDraftUploadFixture(t, 8192)
	for _, test := range []struct {
		name        string
		filename    string
		contentType string
	}{
		{"missing_filename", "", "application/pdf"},
		{"missing_content_type", "report.pdf", ""},
		{"control_filename", "report\r\n.pdf", "application/pdf"},
		{"invalid_utf8", "report\xff.pdf", "application/pdf"},
		{"malformed_content_type", "report.pdf", "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, f.path(), strings.NewReader("attachment"))
			req.Header.Set("Authorization", f.session)
			req.Header.Set("Content-Type", "application/octet-stream")
			req.Header.Set("X-Attachment-Filename", test.filename)
			req.Header.Set("X-Attachment-Content-Type", test.contentType)
			rr := httptest.NewRecorder()
			f.router.ServeHTTP(rr, req)
			if rr.Code != 422 || !strings.Contains(rr.Body.String(), "validation_failed") {
				t.Fatalf("invalid upload metadata = %d %s", rr.Code, rr.Body)
			}
		})
	}
}

// TestDraftAttachmentDeclaredOversizeRefusedBeforeBinding is the API's body cap
// for the upload route, which the general cap exempts: a declared length over
// SMTP_MAX_MESSAGE_SIZE is 413 before the required headers are bound, as the
// E2E body-cap check sends it (TestLimits/BodyCaps, headers and no body).
func TestDraftAttachmentDeclaredOversizeRefusedBeforeBinding(t *testing.T) {
	f := newDraftUploadFixture(t, 8192)
	req := httptest.NewRequest(http.MethodPost, f.path(), strings.NewReader("{}"))
	req.Header.Set("Authorization", f.session)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = 1 << 30
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rr.Body.String(), "message_too_large") {
		t.Fatalf("declared oversize upload = %d %s, want 413 message_too_large", rr.Code, rr.Body)
	}
}
