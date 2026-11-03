package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

func snoozeDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := rmdb.AutoMigrate(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func snoozeCall(t *testing.T, h *MessageHandler, account uint, thread, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	messageRoutes(h).ServeHTTP(recorder, authzRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/accounts/%d/threads/%s/snooze", account, thread), account, nil, body))
	if recorder.Code != status {
		t.Fatalf("snooze: want %d, got %d %s", status, recorder.Code, recorder.Body)
	}
	return recorder
}

func TestThreadSnoozeHidesOnlyFolders(t *testing.T) {
	database := snoozeDB(t)
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedMsgDeleteFixture(t, tx, 123)
	other := models.Mailbox{DomainID: fixture.mailbox.DomainID, Address: "other@delq.test", LocalPart: "other", Active: true}
	if err := tx.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	messages := []models.Message{
		{MailboxID: fixture.mailbox.ID, Folder: "INBOX", ThreadID: "thread", Sender: "sender@example.test", Subject: "snoozable", RawMessage: "original", ReceivedAt: time.Now()},
		{MailboxID: fixture.mailbox.ID, Folder: "Archive", ThreadID: "thread", Sender: "sender@example.test", Subject: "snoozable", ReceivedAt: time.Now()},
		{MailboxID: other.ID, Folder: "INBOX", ThreadID: "thread", Sender: "sender@example.test", Subject: "snoozable", ReceivedAt: time.Now()},
	}
	if err := tx.Create(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Order("id").Where("id IN ?", []uint{messages[0].ID, messages[1].ID, messages[2].ID}).Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	broker := NewSSEBroker()
	first, second := broker.Subscribe(fixture.mailbox.ID), broker.Subscribe(fixture.mailbox.ID)
	defer broker.Unsubscribe(fixture.mailbox.ID, first)
	defer broker.Unsubscribe(fixture.mailbox.ID, second)
	handler := NewMessageHandler(tx, broker, nil, "", nil)
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	due := at.Add(time.Hour)
	setDue := func(value string) {
		t.Helper()
		recorder := httptest.NewRecorder()
		messageRoutes(handler).ServeHTTP(recorder, authzRequest(http.MethodPost,
			fmt.Sprintf("/api/v1/accounts/%d/threads/thread/due", fixture.mailbox.ID), fixture.mailbox.ID, nil, value))
		if recorder.Code != http.StatusOK {
			t.Fatalf("due: %d %s", recorder.Code, recorder.Body)
		}
	}
	setDue(fmt.Sprintf(`{"due":%q,"due_source":"user"}`, due.Format(time.RFC3339)))
	snoozeCall(t, handler, fixture.mailbox.ID, "thread", fmt.Sprintf(`{"snoozed_until":%q}`, at.Format(time.RFC3339)), 200)
	for _, stream := range []chan numberedEvent{first, second} {
		select {
		case event := <-stream:
			if event.Event.Type != "folder_update" {
				t.Fatalf("event = %#v", event)
			}
		default:
			t.Fatal("second device did not receive snooze event")
		}
	}
	for _, folder := range []string{"INBOX", "Archive"} {
		result, err := handler.listMessages(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), listMessagesRequest{ID: uint32(fixture.mailbox.ID), Folder: folder})
		if err != nil || len(result.Data) != 0 || result.Pagination.Total != 0 {
			t.Fatalf("snoozed folder %s: %#v, %v", folder, result, err)
		}
	}
	search, err := NewSearchHandler(tx).search(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), searchRequest{ID: uint32(fixture.mailbox.ID), Q: "snoozable"})
	if err != nil || len(search.Data) != 2 {
		t.Fatalf("search lost snoozed mail: %#v, %v", search, err)
	}
	thread, err := handler.getThread(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), threadRequest{ID: uint32(fixture.mailbox.ID), ThreadID: "thread"})
	if err != nil || len(thread.Data) != 2 {
		t.Fatalf("thread lost snoozed mail: %#v, %v", thread, err)
	}
	for _, message := range append(search.Data, thread.Data...) {
		if message.Due == nil || !message.Due.Equal(due) || message.DueSource == nil || *message.DueSource != "user" || message.SnoozedUntil == nil || !message.SnoozedUntil.Equal(at) {
			t.Fatalf("due and snooze must coexist in search and thread reads: %#v", message)
		}
	}
	detail, err := handler.getMessage(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), messagePath{ID: uint32(messages[0].ID)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(detail)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &fields)
	var deadline time.Time
	if json.Unmarshal(fields["snoozed_until"], &deadline) != nil || !deadline.Equal(at) {
		t.Fatalf("missing durable thread state: %s", encoded)
	}
	if detail.Due == nil || !detail.Due.Equal(due) || detail.DueSource == nil || *detail.DueSource != "user" {
		t.Fatalf("snooze lost the detail's due date: %s", encoded)
	}
	visible, err := handler.listMessages(authzRequest("GET", "/", other.ID, nil, ""), listMessagesRequest{ID: uint32(other.ID), Folder: "INBOX"})
	if err != nil || len(visible.Data) != 1 {
		t.Fatalf("snooze crossed accounts: %#v, %v", visible, err)
	}
	var stored []models.Message
	if err := tx.Order("id").Where("id IN ?", []uint{messages[0].ID, messages[1].ID, messages[2].ID}).Find(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages, stored) {
		t.Fatalf("snooze changed messages: before %#v, after %#v", messages, stored)
	}
	folders, err := handler.listFolders(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), accountPath{ID: uint32(fixture.mailbox.ID)})
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range folders.Data {
		if folder.Total != 0 || folder.Unread != 0 {
			t.Fatalf("snoozed mail counted in folder: %#v", folder)
		}
	}
	due = due.Add(time.Hour)
	setDue(fmt.Sprintf(`{"due":%q,"due_source":"user"}`, due.Format(time.RFC3339)))
	hidden, err := handler.listMessages(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), listMessagesRequest{ID: uint32(fixture.mailbox.ID), Folder: "INBOX"})
	if err != nil || len(hidden.Data) != 0 || hidden.Pagination.Total != 0 {
		t.Fatalf("changing due date revealed a snoozed thread: %#v, %v", hidden, err)
	}
	snoozeCall(t, handler, fixture.mailbox.ID, "thread", `{"snoozed_until":null}`, 200)
	visible, err = handler.listMessages(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), listMessagesRequest{ID: uint32(fixture.mailbox.ID), Folder: "INBOX"})
	if err != nil || len(visible.Data) != 1 {
		t.Fatalf("cancel did not restore: %#v, %v", visible, err)
	}
	if visible.Data[0].Due == nil || !visible.Data[0].Due.Equal(due) || visible.Data[0].DueSource == nil || *visible.Data[0].DueSource != "user" || visible.Data[0].SnoozedUntil != nil {
		t.Fatalf("cancelling snooze lost the updated due date: %#v", visible.Data[0])
	}
	snoozeCall(t, handler, fixture.mailbox.ID, "thread", fmt.Sprintf(`{"snoozed_until":%q}`, at.Format(time.RFC3339)), 200)
	setDue(`{"due":null,"due_source":"user"}`)
	detail, err = handler.getMessage(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), messagePath{ID: uint32(messages[0].ID)})
	if err != nil || detail.Due != nil || detail.DueSource != nil || detail.SnoozedUntil == nil || !detail.SnoozedUntil.Equal(at) {
		t.Fatalf("clearing due date changed the snooze: %#v, %v", detail, err)
	}
}

func TestThreadSnoozeSurvivesRestartAndReturnsOverdue(t *testing.T) {
	cfg := testDBConfig()
	cfg.DBName = "postgres"
	admin, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })
	name := fmt.Sprintf("restmail_snooze_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + name).Error; err != nil {
			t.Error(err)
		}
	})
	cfg.DBName = name
	before, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	beforeSQL, err := before.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = beforeSQL.Close() })
	if err := rmdb.AutoMigrate(before); err != nil {
		t.Fatal(err)
	}
	fixture := seedMsgDeleteFixture(t, before, 0)
	message := models.Message{MailboxID: fixture.mailbox.ID, ThreadID: "restart", Folder: "INBOX", Sender: "sender@example.test"}
	if err := before.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(2 * time.Second).UTC().Truncate(time.Microsecond)
	snoozeCall(t, NewMessageHandler(before, nil, nil, "", nil), fixture.mailbox.ID, "restart", fmt.Sprintf(`{"snoozed_until":%q}`, at.Format(time.RFC3339Nano)), 200)
	if err := beforeSQL.Close(); err != nil {
		t.Fatal(err)
	}
	<-time.After(time.Until(at) + 10*time.Millisecond)
	after, err := rmdb.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	afterSQL, err := after.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = afterSQL.Close() })
	if err := rmdb.AutoMigrate(after); err != nil {
		t.Fatal(err)
	}
	var persisted models.ThreadSnooze
	if err := after.First(&persisted).Error; err != nil || persisted.SnoozedUntil == nil || !persisted.SnoozedUntil.Equal(at) {
		t.Fatalf("restart lost deadline: %#v, %v", persisted, err)
	}
	broker := NewSSEBroker()
	first, second := broker.Subscribe(fixture.mailbox.ID), broker.Subscribe(fixture.mailbox.ID)
	defer broker.Unsubscribe(fixture.mailbox.ID, first)
	defer broker.Unsubscribe(fixture.mailbox.ID, second)
	handler := NewMessageHandler(after, broker, nil, "", nil)
	worker := handler.StartScheduledSender()
	defer worker.Shutdown()
	for _, stream := range []chan numberedEvent{first, second} {
		select {
		case event := <-stream:
			payload, _ := json.Marshal(event.Event.Data)
			if event.Event.Type != "folder_update" || !strings.Contains(string(payload), `"snoozed_until":null`) {
				t.Fatalf("return event = %#v", event)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("startup did not announce overdue thread to both devices")
		}
	}
	visible, err := handler.listMessages(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), listMessagesRequest{ID: uint32(fixture.mailbox.ID), Folder: "INBOX"})
	if err != nil || len(visible.Data) != 1 {
		t.Fatalf("overdue thread did not return: %#v, %v", visible, err)
	}
	handler.returnSnoozedThreads(context.Background())
	select {
	case <-first:
		t.Fatal("returned thread announced twice")
	default:
	}
}

func TestThreadSnoozeReschedulesAndCoversNewMail(t *testing.T) {
	database := snoozeDB(t)
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedMsgDeleteFixture(t, tx, 0)
	message := models.Message{MailboxID: fixture.mailbox.ID, ThreadID: "thread", Folder: "INBOX", Sender: "sender@example.test"}
	if err := tx.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	broker := NewSSEBroker()
	stream := broker.Subscribe(fixture.mailbox.ID)
	defer broker.Unsubscribe(fixture.mailbox.ID, stream)
	handler := NewMessageHandler(tx, broker, nil, "", nil)
	for _, offset := range []time.Duration{time.Hour, 2 * time.Hour} {
		until := time.Now().Add(offset).UTC().Truncate(time.Second)
		snoozeCall(t, handler, fixture.mailbox.ID, "thread", fmt.Sprintf(`{"snoozed_until":%q}`, until.Format(time.RFC3339)), 200)
		<-stream
		var stored models.ThreadSnooze
		if err := tx.Where("mailbox_id = ? AND thread_id = ?", fixture.mailbox.ID, "thread").First(&stored).Error; err != nil || stored.SnoozedUntil == nil || !stored.SnoozedUntil.Equal(until) {
			t.Fatalf("reschedule did not replace deadline: %#v, %v", stored, err)
		}
	}
	newMail := models.Message{MailboxID: fixture.mailbox.ID, ThreadID: "thread", Folder: "INBOX", Sender: "sender@example.test"}
	if err := tx.Create(&newMail).Error; err != nil {
		t.Fatal(err)
	}
	handler.returnSnoozedThreads(context.Background())
	select {
	case <-stream:
		t.Fatal("future thread returned early")
	default:
	}
	visible, err := handler.listMessages(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), listMessagesRequest{ID: uint32(fixture.mailbox.ID), Folder: "INBOX"})
	if err != nil || len(visible.Data) != 0 {
		t.Fatalf("new mail escaped thread snooze: %#v, %v", visible, err)
	}
	if err := tx.Model(&models.ThreadSnooze{}).Where("mailbox_id = ?", fixture.mailbox.ID).Update("snoozed_until", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	handler.returnSnoozedThreads(context.Background())
	select {
	case <-stream:
	default:
		t.Fatal("deadline return not announced")
	}
	visible, err = handler.listMessages(authzRequest("GET", "/", fixture.mailbox.ID, nil, ""), listMessagesRequest{ID: uint32(fixture.mailbox.ID), Folder: "INBOX"})
	if err != nil || len(visible.Data) != 2 || visible.Pagination.Total != 2 {
		t.Fatalf("deadline did not restore entire thread: %#v, %v", visible, err)
	}
}

func TestThreadSnoozeRefusals(t *testing.T) {
	database := snoozeDB(t)
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedMsgDeleteFixture(t, tx, 0)
	message := models.Message{MailboxID: fixture.mailbox.ID, Folder: "INBOX", ThreadID: "thread", Sender: "sender@example.test"}
	if err := tx.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewMessageHandler(tx, nil, nil, "", nil)
	other := models.Mailbox{DomainID: fixture.mailbox.DomainID, LocalPart: "foreign", Address: "foreign@delq.test", Active: true}
	if err := tx.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	foreign := models.Message{MailboxID: other.ID, Folder: "INBOX", ThreadID: "foreign", Sender: "sender@example.test"}
	if err := tx.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"snoozed_until":"yesterday"}`, `{"snoozed_until":"2000-01-01T00:00:00Z"}`, `{"snoozed_until":123}`, `{"unexpected":true}`} {
		snoozeCall(t, handler, fixture.mailbox.ID, "thread", body, 422)
	}
	snoozeCall(t, handler, fixture.mailbox.ID, "missing", `{"snoozed_until":null}`, 404)
	snoozeCall(t, handler, fixture.mailbox.ID, "foreign", `{"snoozed_until":null}`, 404)
	recorder := httptest.NewRecorder()
	messageRoutes(handler).ServeHTTP(recorder, authzRequest("POST", fmt.Sprintf("/api/v1/accounts/%d/threads/thread/snooze", fixture.mailbox.ID), fixture.mailbox.ID+100000, nil, `{"snoozed_until":null}`))
	if recorder.Code != 404 {
		t.Fatalf("out-of-scope snooze: %d %s", recorder.Code, recorder.Body)
	}
}

// A folder listing leaves a snoozed thread out unless include_snoozed=true asks for it, the
// way include_imap_deleted=true asks for IMAP-deleted mail; the two combine (#1599).
func TestListMessagesIncludesSnoozedOnRequest(t *testing.T) {
	database := snoozeDB(t)
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedMsgDeleteFixture(t, tx, 0)
	now := time.Now()
	messages := []models.Message{
		{MailboxID: fixture.mailbox.ID, Folder: "INBOX", ThreadID: "snoozed", Sender: "sender@example.test", Subject: "snoozed", ReceivedAt: now},
		{MailboxID: fixture.mailbox.ID, Folder: "INBOX", ThreadID: "snoozed", Sender: "sender@example.test", Subject: "snoozed, imap-deleted", IMAPDeleted: true, ReceivedAt: now.Add(-time.Minute)},
		{MailboxID: fixture.mailbox.ID, Folder: "INBOX", ThreadID: "awake", Sender: "sender@example.test", Subject: "awake", ReceivedAt: now.Add(-2 * time.Minute)},
	}
	if err := tx.Create(&messages).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewMessageHandler(tx, nil, nil, "", nil)
	at := now.Add(time.Hour).UTC().Truncate(time.Second)
	snoozeCall(t, handler, fixture.mailbox.ID, "snoozed", fmt.Sprintf(`{"snoozed_until":%q}`, at.Format(time.RFC3339)), 200)
	listing := fmt.Sprintf("/api/v1/accounts/%d/folders/INBOX/messages", fixture.mailbox.ID)
	call := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		messageRoutes(handler).ServeHTTP(recorder, authzRequest(http.MethodGet, listing+query, fixture.mailbox.ID, nil, ""))
		return recorder
	}
	list := func(query string, want ...string) {
		t.Helper()
		recorder := call(query)
		var page struct {
			Data       []Message `json:"data"`
			Pagination struct {
				Total int64 `json:"total"`
			} `json:"pagination"`
		}
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &page) != nil {
			t.Fatalf("list %q: %d %s", query, recorder.Code, recorder.Body)
		}
		var got []string
		for _, message := range page.Data {
			got = append(got, message.Subject)
			snoozed := message.ThreadID == "snoozed"
			if snoozed != (message.SnoozedUntil != nil) || (snoozed && !message.SnoozedUntil.Equal(at)) {
				t.Fatalf("list %q: %q carries snoozed_until %v", query, message.Subject, message.SnoozedUntil)
			}
		}
		if !reflect.DeepEqual(got, want) || page.Pagination.Total != int64(len(want)) {
			t.Fatalf("list %q: got %q, total %d; want %q", query, got, page.Pagination.Total, want)
		}
	}
	list("", "awake")
	list("?include_snoozed=false", "awake")
	list("?include_snoozed=true", "snoozed", "awake")
	list("?include_imap_deleted=true", "awake")
	list("?include_snoozed=true&include_imap_deleted=true", "snoozed", "snoozed, imap-deleted", "awake")
	refused := func(name string) string {
		t.Helper()
		recorder := call("?" + name + "=maybe")
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s=maybe: %d %s", name, recorder.Code, recorder.Body)
		}
		return strings.ReplaceAll(recorder.Body.String(), name, "<name>")
	}
	if snoozed, deleted := refused("include_snoozed"), refused("include_imap_deleted"); snoozed != deleted {
		t.Fatalf("a bad include_snoozed is refused unlike include_imap_deleted:\n%s\n%s", snoozed, deleted)
	}
}
