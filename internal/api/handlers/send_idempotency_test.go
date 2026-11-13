package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/sendidempotency"
	"gorm.io/gorm"
)

type sendIdempotencyFixture struct {
	db       *gorm.DB
	route    http.HandlerFunc
	accounts []models.Mailbox
	handler  *MessageHandler
}

func newSendIdempotencyFixture(t *testing.T) sendIdempotencyFixture {
	t.Helper()
	db := draftSendPostgres(t)
	if err := db.AutoMigrate(&models.SendIdempotency{}, &models.ScheduledSend{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "idempotency.example.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	accounts := []models.Mailbox{
		{DomainID: domain.ID, LocalPart: "first", Address: "first@" + domain.Name, Active: true},
		{DomainID: domain.ID, LocalPart: "second", Address: "second@" + domain.Name, Active: true},
	}
	for index := range accounts {
		if err := db.Create(&accounts[index]).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.QuotaUsage{MailboxID: accounts[index].ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedReceiveFixture(t, db, accounts...)
	handler := NewMessageHandler(db, nil, nil, "", nil)
	return sendIdempotencyFixture{db: db, route: composeRoute(handler), accounts: accounts, handler: handler}
}

func (f sendIdempotencyFixture) request(account int, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	f.route(response, sendingAs(request, f.accounts[account].ID))
	return response
}

func (f sendIdempotencyFixture) counts(t *testing.T, sent, queued int64) {
	t.Helper()
	for _, check := range []struct {
		model any
		where string
		want  int64
	}{
		{&models.Message{}, "folder = 'Sent'", sent}, {&models.OutboundQueue{}, "true", queued},
	} {
		var count int64
		if err := f.db.Model(check.model).Where(check.where).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != check.want {
			t.Errorf("%T count=%d want=%d", check.model, count, check.want)
		}
	}
}

func TestSendIdempotencyConcurrentAndSequential(t *testing.T) {
	for _, operation := range []string{"send", "draft", "forward"} {
		t.Run(operation, func(t *testing.T) {
			f := newSendIdempotencyFixture(t)
			observer := &draftSendObserver{check: func() { f.counts(t, 1, 1) }}
			f.handler.broker = observer
			path := "/api/v1/messages/send"
			body := fmt.Sprintf(`{"from":%q,"to":["far@remote.example"],"subject":"one message","body_text":"hello"}`, f.accounts[0].Address)
			if operation != "send" {
				original := models.Message{MailboxID: f.accounts[0].ID, Folder: "Drafts", IsDraft: operation == "draft", Sender: f.accounts[0].Address, Subject: "one message", BodyText: "hello", RecipientsTo: models.JSONB(`["far@remote.example"]`), RecipientsCc: models.JSONB(`[]`)}
				if err := f.db.Create(&original).Error; err != nil {
					t.Fatal(err)
				}
				if operation == "draft" {
					path, body = fmt.Sprintf("/api/v1/messages/draft/%d/send", original.ID), ""
				} else {
					path = fmt.Sprintf("/api/v1/messages/%d/forward", original.ID)
					body = fmt.Sprintf(`{"from":%q,"to":["far@remote.example"],"body_text":"hello"}`, f.accounts[0].Address)
				}
			}
			start := make(chan struct{})
			results := make([]*httptest.ResponseRecorder, 8)
			var workers sync.WaitGroup
			for index := range results {
				workers.Go(func() { <-start; results[index] = f.request(0, path, body, "same-key") })
			}
			close(start)
			workers.Wait()
			for _, response := range results {
				if response.Code != 201 {
					t.Fatalf("send: %d %s", response.Code, response.Body)
				}
				if response.Body.String() != results[0].Body.String() {
					t.Errorf("concurrent replay changed response: %s != %s", response.Body, results[0].Body)
				}
			}
			if operation == "forward" {
				if err := f.db.Where("folder = ?", "Drafts").Delete(&models.Message{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := f.db.Model(&models.Message{}).Where("folder = ?", "Sent").UpdateColumn("subject", "edited after send").Error; err != nil {
				t.Fatal(err)
			}
			replay := f.request(0, path, body, "same-key")
			if replay.Code != 201 || replay.Body.String() != results[0].Body.String() {
				t.Fatalf("sequential replay: %d %s", replay.Code, replay.Body)
			}
			f.counts(t, 1, 1)
			if observer.events != 1 {
				t.Fatalf("sent notifications=%d want=1", observer.events)
			}
		})
	}
}

func TestSendIdempotencyWaitCancellationAndRollback(t *testing.T) {
	f := newSendIdempotencyFixture(t)
	body := fmt.Sprintf(`{"from":%q,"to":["far@remote.example"]}`, f.accounts[0].Address)
	entered, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	if err := f.db.Callback().Create().Before("gorm:create").Register("idempotency_block_send", func(tx *gorm.DB) {
		if tx.Statement.Table == "messages" {
			first.Do(func() { close(entered); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Callback().Create().Remove("idempotency_block_send"); err != nil {
			t.Error(err)
		}
	})
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- f.request(0, "/api/v1/messages/send", body, "wait-key") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("first send did not enter transaction")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "wait-key")
	response := httptest.NewRecorder()
	f.route(response, sendingAs(request, f.accounts[0].ID))
	close(release)
	if response.Code != 503 || !strings.Contains(response.Body.String(), `"code":"deferred"`) {
		t.Fatalf("cancelled waiter: %d %s", response.Code, response.Body)
	}
	select {
	case response := <-finished:
		if response.Code != 201 {
			t.Fatalf("first send: %d %s", response.Code, response.Body)
		}
		if replay := f.request(0, "/api/v1/messages/send", body, "wait-key"); replay.Code != 201 || replay.Body.String() != response.Body.String() {
			t.Fatalf("replay after wait: %d %s", replay.Code, replay.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first send did not finish")
	}
	f.counts(t, 1, 1)
}

func TestSendIdempotencyStorageFailureRollsBackSend(t *testing.T) {
	for _, table := range []string{"outbound_queue", "send_idempotencies"} {
		t.Run(table, func(t *testing.T) {
			f := newSendIdempotencyFixture(t)
			observer := &draftSendObserver{check: func() { f.counts(t, 1, 1) }}
			f.handler.broker = observer
			for _, sql := range []string{
				`CREATE FUNCTION refuse_send() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private injected failure'; END $$`,
				`CREATE TRIGGER refuse_send BEFORE INSERT ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION refuse_send()`,
			} {
				if err := f.db.Exec(sql).Error; err != nil {
					t.Fatal(err)
				}
			}
			body := fmt.Sprintf(`{"from":%q,"to":["far@remote.example"]}`, f.accounts[0].Address)
			response := f.request(0, "/api/v1/messages/send", body, "retry-key")
			if response.Code != 503 || strings.Contains(response.Body.String(), "private") {
				t.Fatalf("failed send: %d %s", response.Code, response.Body)
			}
			f.counts(t, 0, 0)
			var receipts int64
			if err := f.db.Model(&models.SendIdempotency{}).Count(&receipts).Error; err != nil {
				t.Fatal(err)
			}
			if receipts != 0 || observer.events != 0 {
				t.Fatalf("rolled back receipts=%d events=%d", receipts, observer.events)
			}
			if err := f.db.Exec(`DROP TRIGGER refuse_send ON ` + table).Error; err != nil {
				t.Fatal(err)
			}
			if response := f.request(0, "/api/v1/messages/send", body, "retry-key"); response.Code != 201 {
				t.Fatalf("retry after rollback: %d %s", response.Code, response.Body)
			}
			f.counts(t, 1, 1)
		})
	}
}

func TestSendIdempotencyScheduledAndNoKey(t *testing.T) {
	f := newSendIdempotencyFixture(t)
	body := fmt.Sprintf(`{"from":%q,"to":["far@remote.example"]}`, f.accounts[0].Address)
	for range 2 {
		if response := f.request(0, "/api/v1/messages/send", body, ""); response.Code != 201 {
			t.Fatalf("without key: %d %s", response.Code, response.Body)
		}
	}
	f.counts(t, 2, 2)
	scheduled := strings.TrimSuffix(body, "}") + fmt.Sprintf(`,"scheduled_at":%q}`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
	first := f.request(0, "/api/v1/messages/send", scheduled, "scheduled-key")
	replay := f.request(0, "/api/v1/messages/send", scheduled, "scheduled-key")
	if first.Code != 202 || replay.Code != 202 || first.Body.String() != replay.Body.String() {
		t.Fatalf("scheduled: %d %s; replay: %d %s", first.Code, first.Body, replay.Code, replay.Body)
	}
	var count int64
	if err := f.db.Model(&models.ScheduledSend{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("scheduled sends=%d", count)
	}
	f.counts(t, 2, 2)
	if err := f.db.Model(&models.SendIdempotency{}).Where("account_id = ?", f.accounts[0].ID).Update("expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if got := sendidempotency.NewPurger(f.db, time.Hour).PurgeOnce(time.Now()); got != 1 {
		t.Fatalf("purged=%d want=1", got)
	}
	if err := f.db.Model(&models.SendIdempotency{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expired keys retained=%d", count)
	}
}

func TestSendIdempotencyReplayNeedsCurrentPermissions(t *testing.T) {
	f := newSendIdempotencyFixture(t)
	body := fmt.Sprintf(`{"from":%q,"to":["far@remote.example"]}`, f.accounts[0].Address)
	if response := f.request(0, "/api/v1/messages/send", body, "permission-key"); response.Code != 201 {
		t.Fatalf("first send: %d %s", response.Code, response.Body)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "permission-key")
	request = withClaims(request, &auth.Claims{AccountID: f.accounts[0].ID, CredentialKind: auth.CredentialKindAPIKey, APIKeyPermissions: []auth.ScopedPermission{{Capability: models.CapMailRead, Scope: "mailbox:" + f.accounts[0].Address}}})
	response := httptest.NewRecorder()
	f.route(response, request)
	if response.Code != 403 {
		t.Fatalf("read-only replay: %d %s", response.Code, response.Body)
	}
	f.counts(t, 1, 1)
}

func TestSendIdempotencyPurgerKeepsLiveKeys(t *testing.T) {
	f := newSendIdempotencyFixture(t)
	for account := range f.accounts {
		body := fmt.Sprintf(`{"from":%q,"to":["far@remote.example"]}`, f.accounts[account].Address)
		if response := f.request(account, "/api/v1/messages/send", body, "purge-key"); response.Code != 201 {
			t.Fatalf("first send: %d %s", response.Code, response.Body)
		}
	}
	now := time.Now()
	if err := f.db.Model(&models.SendIdempotency{}).Where("account_id = ?", f.accounts[0].ID).Update("expires_at", now).Error; err != nil {
		t.Fatal(err)
	}
	if got := sendidempotency.NewPurger(f.db, time.Hour).PurgeOnce(now); got != 1 {
		t.Fatalf("purged=%d want=1", got)
	}
	var receipts []models.SendIdempotency
	if err := f.db.Find(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].AccountID != f.accounts[1].ID {
		t.Fatalf("live receipts=%+v", receipts)
	}
}

func TestSendIdempotencyKeyValidation(t *testing.T) {
	for _, path := range []string{"/api/v1/messages/send", "/api/v1/messages/draft/1/send", "/api/v1/messages/1/forward"} {
		for _, keys := range [][]string{{strings.Repeat("k", 256)}, {"first", "second"}} {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			request.Header.Set("Content-Type", "application/json")
			for _, key := range keys {
				request.Header.Add("Idempotency-Key", key)
			}
			response := httptest.NewRecorder()
			composeRoute(&MessageHandler{})(response, sendingAs(request, 1))
			if response.Code != 422 {
				t.Fatalf("%s: invalid key: %d %s", path, response.Code, response.Body)
			}
		}
	}
}

func TestSendIdempotencyAccountScopeExpiryAndDifferentBody(t *testing.T) {
	f := newSendIdempotencyFixture(t)
	for account := range f.accounts {
		body := fmt.Sprintf(`{"from":%q,"to":["far@remote.example"],"subject":"original"}`, f.accounts[account].Address)
		first := f.request(account, "/api/v1/messages/send", body, "account-key")
		if first.Code != 201 {
			t.Fatalf("first: %d %s", first.Code, first.Body)
		}
		changed := f.request(account, "/api/v1/messages/send", strings.Replace(body, "original", "changed", 1), "account-key")
		if changed.Code != 409 {
			t.Fatalf("different body: %d %s", changed.Code, changed.Body)
		}
		var receipt models.SendIdempotency
		if err := f.db.Where("account_id = ?", f.accounts[account].ID).First(&receipt).Error; err != nil {
			t.Fatal(err)
		}
		if remaining := time.Until(receipt.ExpiresAt); remaining < 24*time.Hour-time.Minute || remaining > 24*time.Hour {
			t.Fatalf("retention=%v", remaining)
		}
	}
	f.counts(t, 2, 2)
	if err := f.db.Model(&models.SendIdempotency{}).Where("account_id = ?", f.accounts[0].ID).Update("expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"from":%q,"to":["far@remote.example"],"subject":"after expiry"}`, f.accounts[0].Address)
	response := f.request(0, "/api/v1/messages/send", body, "account-key")
	if response.Code != 201 {
		t.Fatalf("expired key: %d %s", response.Code, response.Body)
	}
	f.counts(t, 3, 3)
}
