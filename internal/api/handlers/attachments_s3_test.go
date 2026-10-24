package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/attachmentstore/s3test"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

func TestS3AttachmentAuthorizationAndDownload(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	msg := models.Message{MailboxID: fx.mailbox.ID, Folder: "INBOX", Sender: "sender@remote.test"}
	if err := tx.Create(&msg).Error; err != nil {
		t.Fatal(err)
	}
	fake := s3test.New(t)
	store := attachmentstore.NewS3(fake.Config)
	ref, sum, err := store.Put(context.Background(), []byte("private attachment"))
	if err != nil {
		t.Fatal(err)
	}
	att := models.Attachment{MessageID: msg.ID, Filename: "sender.html", StorageType: "s3", StorageRef: ref, Checksum: sum}
	if err := tx.Create(&att).Error; err != nil {
		t.Fatal(err)
	}
	download := attachmentDownload(NewAttachmentHandler(tx, store))
	path := "/api/v1/attachments/" + uintToStr(att.ID)
	before := fake.Requests.Load()
	for _, account := range []uint{fx.mailbox.ID + 100, 0} {
		w := httptest.NewRecorder()
		download(w, withSession(httptest.NewRequest(http.MethodGet, path, nil), account))
		want := http.StatusNotFound
		if account == 0 {
			want = http.StatusUnauthorized
		}
		if w.Code != want || fake.Requests.Load() != before {
			t.Fatalf("unauthorized storage access: status=%d requests=%d", w.Code, fake.Requests.Load()-before)
		}
	}
	w := httptest.NewRecorder()
	r := withSession(httptest.NewRequest(http.MethodGet, path, nil), fx.mailbox.ID)
	r.Header.Set("Range", "bytes=8-17")
	download(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "attachment" || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("Content-Disposition") != "attachment; filename=sender.html" {
		t.Fatalf("authorized range download: status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
	fake.Failure.Store(403)
	w = httptest.NewRecorder()
	download(w, withSession(httptest.NewRequest(http.MethodGet, path, nil), fx.mailbox.ID))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("storage failure was acknowledged: %d", w.Code)
	}
}

// attachmentDownload is downloading an attachment through its registration.
func attachmentDownload(h *AttachmentHandler) http.HandlerFunc {
	routes := chi.NewRouter()
	h.Register(endpoint.Routes{Router: routes, Registry: endpoint.NewRegistry()})
	return routes.ServeHTTP
}

// withSession is r with a session for accountID, or none for 0.
func withSession(r *http.Request, accountID uint) *http.Request {
	if accountID == 0 {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: accountID}))
}
