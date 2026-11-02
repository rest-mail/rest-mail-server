package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

func TestThreadDueLifecycle(t *testing.T) {
	database, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := rmdb.AutoMigrate(database); err != nil {
		t.Fatal(err)
	}
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedAuthzFixture(t, tx)
	seedReceiveFixture(t, tx, fixture.ownerMailbox, fixture.attackerMailbox)
	owner, other := fixture.ownerMailbox.ID, fixture.attackerMailbox.ID
	rows := []models.Message{
		{MailboxID: owner, Folder: "INBOX", ThreadID: "due-thread", Sender: "s@example.test", Subject: "due test", ReceivedAt: time.Now().Add(-time.Hour)},
		{MailboxID: owner, Folder: "INBOX", ThreadID: "other-thread", Sender: "s@example.test", Subject: "due test", ReceivedAt: time.Now()},
		{MailboxID: other, Folder: "INBOX", ThreadID: "due-thread", Sender: "s@example.test", Subject: "due test", ReceivedAt: time.Now()},
	}
	if err := tx.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewMessageHandler(tx, nil, nil, "", nil)
	call := func(actor, account uint, thread, body string, want int) map[string]json.RawMessage {
		t.Helper()
		response := httptest.NewRecorder()
		request := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/threads/%s/due", account, thread), actor, nil, body)
		request.Header.Set("Content-Type", "application/json")
		messageRoutes(handler).ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("due %s: got %d %s, want %d", body, response.Code, response.Body, want)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	list := func(account uint) []Message {
		t.Helper()
		result, err := handler.listMessages(authzRequest("GET", "/", account, nil, ""), listMessagesRequest{ID: uint32(account), Folder: "INBOX"})
		if err != nil {
			t.Fatal(err)
		}
		return result.Data
	}
	before := list(owner)
	due := "2026-12-01T10:00:00Z"
	result := call(owner, owner, "due-thread", `{"due":"`+due+`","due_source":"user"}`, 200)
	var state struct {
		ThreadID string     `json:"thread_id"`
		Due      *time.Time `json:"due"`
		Source   *string    `json:"due_source"`
	}
	if err := json.Unmarshal(result["data"], &state); err != nil || state.Due == nil || state.Source == nil || *state.Source != "user" || state.Due.Format(time.RFC3339) != due {
		t.Fatalf("due response = %#v, %v", state, err)
	}
	for _, source := range []string{"rule", "parsed", "unknown"} {
		call(owner, owner, "due-thread", `{"due":"`+due+`","due_source":"`+source+`"}`, 422)
	}
	for _, invalid := range []string{"bad", "2026-02-30T10:00:00Z", "2026-12-01", ""} {
		call(owner, owner, "due-thread", fmt.Sprintf(`{"due":%q,"due_source":"user"}`, invalid), 422)
	}
	call(owner, owner, "missing", `{"due":null,"due_source":"user"}`, 404)
	call(other, owner, "due-thread", `{"due":null,"due_source":"user"}`, 404)
	call(other, other, "other-thread", `{"due":null,"due_source":"user"}`, 404)
	readOnly := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/threads/due-thread/due", owner), owner, nil, `{"due":null,"due_source":"user"}`)
	claims := freshTwoFactorClaims(owner, fixture.ownerMailbox.Address)
	claims.CredentialKind = auth.CredentialKindAPIKey
	claims.APIKeyPermissions = []auth.ScopedPermission{{Capability: models.CapMailRead, Scope: "mailbox:" + fixture.ownerMailbox.Address}}
	readOnly = readOnly.WithContext(context.WithValue(readOnly.Context(), middleware.ClaimsKey, claims))
	refused := httptest.NewRecorder()
	messageRoutes(handler).ServeHTTP(refused, readOnly)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("read-only caller: %d %s", refused.Code, refused.Body)
	}
	var durable models.ThreadDue
	if err := tx.First(&durable, "mailbox_id = ? AND thread_id = ?", owner, "due-thread").Error; err != nil || durable.Due == nil || durable.DueSource == nil || *durable.DueSource != "user" {
		t.Fatalf("due was not stored: %#v, %v", durable, err)
	}
	thread, err := handler.getThread(authzRequest("GET", "/", owner, nil, ""), threadRequest{ID: uint32(owner), ThreadID: "due-thread"})
	if err != nil || len(thread.Data) != 1 {
		t.Fatalf("thread = %#v, %v", thread, err)
	}
	encoded, _ := json.Marshal(thread.Data[0])
	if err := json.Unmarshal(encoded, &state); err != nil || state.Source == nil || *state.Source != "user" || state.Due == nil {
		t.Fatalf("thread lost due/source: %s", encoded)
	}
	detail, err := handler.getMessage(authzRequest("GET", "/", owner, nil, ""), messagePath{ID: uint32(rows[0].ID)})
	if err != nil {
		t.Fatal(err)
	}
	detailJSON, _ := json.Marshal(detail)
	var detailFields map[string]json.RawMessage
	_ = json.Unmarshal(detailJSON, &detailFields)
	if string(detailFields["due_source"]) != `"user"` {
		t.Fatalf("detail lost source: %s", detailJSON)
	}
	ids := func(messages []Message) []uint {
		out := make([]uint, len(messages))
		for index, message := range messages {
			out[index] = message.ID
		}
		return out
	}
	if after := list(owner); !reflect.DeepEqual(ids(before), ids(after)) {
		t.Fatalf("due reordered list: %v -> %v", ids(before), ids(after))
	}
	otherJSON, _ := json.Marshal(list(other))
	var otherRows []map[string]json.RawMessage
	_ = json.Unmarshal(otherJSON, &otherRows)
	if source := otherRows[0]["due_source"]; len(source) != 0 && string(source) != "null" {
		t.Fatalf("cross-account due leak: %s", otherJSON)
	}
	result = call(owner, owner, "due-thread", `{"due":null,"due_source":"user"}`, 200)
	if err := json.Unmarshal(result["data"], &state); err != nil || state.Due != nil || state.Source != nil {
		t.Fatalf("clear = %#v, %v", state, err)
	}
	call(owner, owner, "due-thread", `{"due":null,"due_source":"user"}`, 200)
	durable = models.ThreadDue{}
	if err := tx.First(&durable, "mailbox_id = ? AND thread_id = ?", owner, "due-thread").Error; err != nil || durable.Due != nil || durable.DueSource != nil {
		t.Fatalf("clear was not stored: %#v, %v", durable, err)
	}
	var stored []models.Message
	if err := tx.Order("id").Find(&stored).Error; err != nil || len(stored) != len(rows) {
		t.Fatalf("messages changed: %#v, %v", stored, err)
	}
	for index, message := range stored {
		if message.Folder != rows[index].Folder || message.IsDeleted || !message.ReceivedAt.Equal(rows[index].ReceivedAt.Truncate(time.Microsecond)) {
			t.Fatalf("due filed or changed message: %#v", message)
		}
	}
}

func TestThreadDueMigrationAndPersistence(t *testing.T) {
	database, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := rmdb.AutoMigrate(database); err != nil {
		t.Fatal(err)
	}
	thread := fmt.Sprintf("due-persistence-%d", time.Now().UnixNano())
	t.Cleanup(func() { database.Where("mailbox_id = ? AND thread_id = ?", 987654, thread).Delete(&models.ThreadDue{}) })
	due := time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC)
	for _, source := range []string{"user", "rule", "parsed"} {
		row := models.ThreadDue{MailboxID: 987654, ThreadID: thread, Due: &due, DueSource: &source}
		if err := database.Save(&row).Error; err != nil {
			t.Fatal(err)
		}
		fresh, err := rmdb.Connect(testDBConfig())
		if err != nil {
			t.Fatal(err)
		}
		pool, err := fresh.DB()
		if err != nil {
			t.Fatal(err)
		}
		var stored models.ThreadDue
		err = fresh.First(&stored, "mailbox_id = ? AND thread_id = ?", row.MailboxID, thread).Error
		_ = pool.Close()
		if err != nil || stored.Due == nil || !stored.Due.Equal(due) || stored.DueSource == nil || *stored.DueSource != source {
			t.Fatalf("state did not survive reconnect: %#v, %v", stored, err)
		}
	}
	for _, row := range []models.ThreadDue{
		{MailboxID: 987654, ThreadID: thread, Due: &due},
		{MailboxID: 987654, ThreadID: thread, Due: &due, DueSource: dueSourcePointer("invented")},
		{MailboxID: 987654, ThreadID: thread, DueSource: dueSourcePointer("user")},
	} {
		if err := database.Transaction(func(tx *gorm.DB) error { return tx.Save(&row).Error }); err == nil {
			t.Fatalf("migration accepted invalid state: %#v", row)
		}
	}
}

func dueSourcePointer(source string) *string { return &source }
