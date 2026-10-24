package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// A folder lists newest first by received_at, the id breaking ties, and its pages hold every
// message exactly once. A copy keeps its source's received_at under a newer, higher id, and
// the cursor used to page by id alone, so a copy older than a page's cut was never listed
// (#805).
func TestListMessages_PagesEveryMessageOnce(t *testing.T) {
	db := openMsgDeleteTestDB(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fx := seedMsgDeleteFixture(t, tx, 0)

	// Ids ascend in creation order and dates do not: the fourth is a copy of the first.
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var ids []uint
	for i, received := range []time.Time{base, base.Add(time.Hour), base.Add(2 * time.Hour), base, base.Add(3 * time.Hour)} {
		msg := models.Message{MailboxID: fx.mailbox.ID, Folder: "INBOX", Sender: "sender@delq.test", Subject: fmt.Sprint(i), ReceivedAt: received}
		if err := tx.Create(&msg).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}

	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: fx.mailbox.ID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	NewMessageHandler(tx, nil, nil, "", nil).RegisterMessages(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})

	var got []uint
	cursor := ""
	for page := 0; ; page++ {
		if page > len(ids) {
			t.Fatalf("pages never end: %v", got)
		}
		target := fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages?limit=2", fx.mailbox.ID)
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("page %d: %d %s", page, rr.Code, rr.Body)
		}
		var body struct {
			Data []struct {
				ID uint `json:"id"`
			} `json:"data"`
			Pagination struct {
				Cursor  string `json:"cursor"`
				HasMore bool   `json:"has_more"`
			} `json:"pagination"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, m := range body.Data {
			got = append(got, m.ID)
		}
		if !body.Pagination.HasMore {
			break
		}
		cursor = body.Pagination.Cursor
	}
	if want := []uint{ids[4], ids[2], ids[1], ids[3], ids[0]}; !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v: newest first, each message once", got, want)
	}
}

// A cursor that does not carry both the date and the id, the form before #805 among them,
// is not a cursor.
func TestDecodeMessageCursor(t *testing.T) {
	for _, cursor := range []string{"", "not base64!", "e30=", "eyJpZCI6NX0="} {
		if _, ok := decodeMessageCursor(cursor); ok {
			t.Errorf("%q decoded as a cursor", cursor)
		}
	}
	at := time.Date(2026, 10, 1, 12, 0, 0, 123000, time.UTC)
	raw, _ := json.Marshal(messageCursor{ReceivedAt: at, ID: 7})
	after, ok := decodeMessageCursor(base64.StdEncoding.EncodeToString(raw))
	if !ok || after.ID != 7 || !after.ReceivedAt.Equal(at) {
		t.Fatalf("round trip: %+v %t", after, ok)
	}
}
