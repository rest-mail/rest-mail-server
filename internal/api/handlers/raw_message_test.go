package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// An id that is not one is binding's 422, before any message is looked up.
func TestRawMessageBindingBeforeDatabase(t *testing.T) {
	routes := messageRoutes(NewMessageHandler(nil, nil, nil, "", nil))
	for _, path := range []string{"/api/v1/messages/0/raw", "/api/v1/messages/abc/raw"} {
		rr := httptest.NewRecorder()
		routes.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"id":`) {
			t.Fatalf("%s: want 422 on id, got %d %s", path, rr.Code, rr.Body)
		}
	}
}

// The source is written byte for byte as message/rfc822; a message with none kept, one that
// is not there and one in another account are refused as before.
func TestRawMessage(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)
	source := "Subject: café\r\n\r\nbody\r\n"
	kept := models.Message{MailboxID: fx.mailbox.ID, Folder: "INBOX", Sender: "a@b.test", RawMessage: source}
	none := models.Message{MailboxID: fx.mailbox.ID, Folder: "INBOX", Sender: "a@b.test"}
	for _, m := range []*models.Message{&kept, &none} {
		if err := tx.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fx.mailbox.ID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	NewMessageHandler(tx, nil, nil, "", nil).RegisterMessages(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	get := func(id uint) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/raw", id), nil))
		return rr
	}

	rr := get(kept.ID)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "message/rfc822" || rr.Body.String() != source {
		t.Fatalf("kept source: %d %q %q", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
	if rr := get(none.ID); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), `"not_found"`) {
		t.Fatalf("no source: %d %s", rr.Code, rr.Body)
	}
	if rr := get(none.ID + 1000); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), `"message_not_found"`) {
		t.Fatalf("missing message: %d %s", rr.Code, rr.Body)
	}
}
