package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An id that is not one is binding's 422, and a request without a session the handler's 401,
// before any attachment is looked up: a handler with no database proves it.
func TestAttachmentDownloadBeforeDatabase(t *testing.T) {
	download := attachmentDownload(NewAttachmentHandler(nil, nil))
	for _, path := range []string{"/api/v1/attachments/0", "/api/v1/attachments/abc"} {
		rr := httptest.NewRecorder()
		download(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"id":`) {
			t.Fatalf("%s: want 422 on id, got %d %s", path, rr.Code, rr.Body)
		}
	}
	rr := httptest.NewRecorder()
	download(rr, httptest.NewRequest(http.MethodGet, "/api/v1/attachments/7", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no session: want 401, got %d %s", rr.Code, rr.Body)
	}
}
