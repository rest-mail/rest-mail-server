package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type classificationQueryCounter struct {
	logger.Interface
	count atomic.Int64
}

func (counter *classificationQueryCounter) Trace(ctx context.Context, begin time.Time, query func() (string, int64), err error) {
	counter.count.Add(1)
	counter.Interface.Trace(ctx, begin, query, err)
}

func TestMessageClassificationStoredHeaders(t *testing.T) {
	for _, headers := range []models.JSONB{nil, models.JSONB(`[]`), models.JSONB(`null`), models.JSONB(`{"List-Id":42}`), models.JSONB(`{"List-Id":["<news.example.test>"],"bad":false}`), models.JSONB(`broken`)} {
		if got := messageOf(models.Message{Headers: headers}).Class; got != "person" {
			t.Fatalf("headers %s: got %s", headers, got)
		}
	}
	if got := messageOf(models.Message{Headers: models.JSONB(`{"List-Id":["<news.example.test>"]}`)}).Class; got != "newsletter" {
		t.Fatalf("stored list class: %s", got)
	}
}

func TestMessageClassificationAPIAndConstantQueries(t *testing.T) {
	db, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(append(accountModels, &models.Message{})...); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	domain := models.Domain{Name: fmt.Sprintf("classification-%d.test", time.Now().UnixNano())}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "owner", Address: "owner@" + domain.Name, Active: true, QuotaBytes: 1 << 30}
	if err := tx.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, tx, mailbox)
	counter := &classificationQueryCounter{Interface: logger.Default.LogMode(logger.Silent)}
	counted := tx.Session(&gorm.Session{Logger: counter})
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			ctx := context.WithValue(request.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: mailbox.ID})
			next.ServeHTTP(w, request.WithContext(ctx))
		})
	})
	NewMessageHandler(counted, nil, nil, "", nil).RegisterMessages(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
	listPath := fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages", mailbox.ID)
	read := func(path string) []byte {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, recorder.Code, recorder.Body)
		}
		return recorder.Body.Bytes()
	}
	want := map[uint]string{}
	var oneMessageQueries int64
	for index, sample := range []struct{ headers, class string }{
		{`{"Precedence":["personal"]}`, "person"},
		{`{"List-Id":["<news.example.test>"]}`, "newsletter"},
		{`{"List-Unsubscribe":["<https://example.test/leave>"]}`, "newsletter"},
		{`{"Auto-Submitted":["auto-generated"]}`, "notification"},
	} {
		message := models.Message{MailboxID: mailbox.ID, Folder: "INBOX", Headers: models.JSONB(sample.headers)}
		if err := tx.Create(&message).Error; err != nil {
			t.Fatal(err)
		}
		want[message.ID] = sample.class
		var single struct{ Data Message }
		if err := json.Unmarshal(read(fmt.Sprintf("/api/v1/messages/%d", message.ID)), &single); err != nil {
			t.Fatal(err)
		}
		if single.Data.Class != sample.class {
			t.Fatalf("single class = %s, want %s", single.Data.Class, sample.class)
		}
		counter.count.Store(0)
		var list struct{ Data []Message }
		if err := json.Unmarshal(read(listPath), &list); err != nil {
			t.Fatal(err)
		}
		queries := counter.count.Load()
		if index == 0 {
			oneMessageQueries = queries
		}
		if queries != oneMessageQueries || queries < 2 {
			t.Fatalf("%d messages: %d queries, one message used %d", index+1, queries, oneMessageQueries)
		}
		if len(list.Data) != len(want) {
			t.Fatalf("list length = %d, want %d", len(list.Data), len(want))
		}
		for _, item := range list.Data {
			if item.Class != want[item.ID] {
				t.Fatalf("list message %d: class = %s, want %s", item.ID, item.Class, want[item.ID])
			}
		}
		t.Logf("%d stored messages, %d list queries", index+1, queries)
	}
}
