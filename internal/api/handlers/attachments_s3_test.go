package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/attachmentstore/s3test"
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
	h := NewAttachmentHandler(tx, store)
	params := map[string]string{"id": uintToStr(att.ID)}
	before := fake.Requests.Load()
	for _, account := range []uint{fx.mailbox.ID + 100, 0} {
		w := httptest.NewRecorder()
		r := authzRequest(http.MethodGet, "/attachments/"+params["id"], account, params, "")
		if account == 0 {
			r = httptest.NewRequest(http.MethodGet, "/attachments/1", nil)
		}
		h.GetAttachment(w, r)
		want := http.StatusNotFound
		if account == 0 {
			want = http.StatusUnauthorized
		}
		if w.Code != want || fake.Requests.Load() != before {
			t.Fatalf("unauthorized storage access: status=%d requests=%d", w.Code, fake.Requests.Load()-before)
		}
	}
	w := httptest.NewRecorder()
	r := authzRequest(http.MethodGet, "/attachments/"+params["id"], fx.mailbox.ID, params, "")
	r.Header.Set("Range", "bytes=8-17")
	h.GetAttachment(w, r)
	if w.Code != http.StatusPartialContent || w.Body.String() != "attachment" || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("Content-Disposition") != "attachment; filename=sender.html" {
		t.Fatalf("authorized range download: status=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
	fake.Failure.Store(403)
	w = httptest.NewRecorder()
	h.GetAttachment(w, authzRequest(http.MethodGet, "/attachments/"+params["id"], fx.mailbox.ID, params, ""))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("storage failure was acknowledged: %d", w.Code)
	}
}
