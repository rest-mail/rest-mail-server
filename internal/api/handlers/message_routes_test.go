package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// A message is written as the model wrote it, its raw source never: the gateways, webmail and
// the mobile app all read these bytes.
func TestMessageResponseJSONParity(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	encrypted := true
	full := models.Message{ID: 3, MailboxID: 4, Folder: "INBOX", MsgID: "<a@b>", InReplyTo: "<x@y>", References: "<x@y>",
		ThreadID: "t1", Sender: "s@example.test", SenderName: "S", RecipientsTo: models.JSONB(`["a@example.test"]`),
		RecipientsCc: models.JSONB(`[]`), Subject: "Hi", BodyText: "text", BodyHTML: "<p>html</p>",
		Headers: models.JSONB(`{"X-A":["1"]}`), RawMessage: "secret-raw", CalendarEventsRaw: models.JSONB(`[{"uid":"1"}]`),
		SizeBytes: 10, RawSize: 12, ReceivedTLS: &encrypted, TLSVersion: "TLS1.3", HasAttachments: true, IsRead: true,
		IsDSN: true, IsFlagged: true, IsStarred: true, IsDraft: true, IsDeleted: true, ReceivedAt: now, DateHeader: &now,
		CreatedAt: now, UpdatedAt: now}
	for _, tc := range []struct {
		name         string
		old, current interface{}
	}{
		{"every field", full, messageOf(full)},
		{"empty message", models.Message{}, messageOf(models.Message{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old, err := json.Marshal(tc.old)
			if err != nil {
				t.Fatal(err)
			}
			current, err := json.Marshal(tc.current)
			if err != nil {
				t.Fatal(err)
			}
			// An unloaded mailbox's domain is a zero-filled record in the model's JSON;
			// the answer omits it rather than send an invalid record (#912, #963).
			old = withoutUnloadedMailboxDomain(t, old)
			if string(old) != string(current) {
				t.Fatalf("response JSON changed:\nold %s\nnew %s", old, current)
			}
			if strings.Contains(string(current), "secret-raw") {
				t.Fatalf("raw message answered: %s", current)
			}
		})
	}
}

// withoutUnloadedMailboxDomain drops mailbox.domain from a model's JSON when the domain was
// never loaded (its id is 0), keeping key order for the comparison.
func withoutUnloadedMailboxDomain(t *testing.T, data []byte) []byte {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	mailbox, ok := raw["mailbox"]
	if !ok {
		return data
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(mailbox, &fields); err != nil {
		t.Fatal(err)
	}
	var domain struct {
		ID uint `json:"id"`
	}
	if d, ok := fields["domain"]; !ok || json.Unmarshal(d, &domain) != nil || domain.ID != 0 {
		return data
	}
	i := strings.Index(string(data), `,"domain":{"id":0,`)
	if i < 0 {
		t.Fatalf("unloaded mailbox domain not found in %s", data)
	}
	end := i + strings.Index(string(data[i:]), "}") + 1
	return append(append([]byte{}, data[:i]...), data[end:]...)
}

// An attachment's answer says what it is, never where it is stored (#658): no path on the
// server's disk or S3 key, and no storage backend.
func TestAttachmentAnswerLeavesOutStorage(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	attachment := models.Attachment{ID: 5, MessageID: 3, Filename: "a.pdf", ContentType: "application/pdf", SizeBytes: 99,
		StorageType: "s3", StorageRef: "bucket/key", Checksum: "abc", CreatedAt: now}
	got, err := json.Marshal(attachmentOf(attachment))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":5,"message_id":3,"filename":"a.pdf","content_type":"application/pdf","size_bytes":99,"checksum":"abc","created_at":"2026-01-02T03:04:05Z"}`
	if string(got) != want {
		t.Fatalf("attachment answered as %s, want %s", got, want)
	}
}

// What binding refuses, it refuses before anything is looked up: a nil database proves
// nothing is reached. Bound, a request without a session is the handler's 401.
func TestMessageBindingBeforeDatabase(t *testing.T) {
	h := NewMessageHandler(nil, nil, nil, "", nil)
	routes := messageRoutes(h)
	search := searchRoutes(NewSearchHandler(nil))
	for _, tc := range []struct {
		handler                   http.Handler
		method, path, body, field string
	}{
		{routes, http.MethodGet, "/api/v1/messages/0", "", "id"},
		{routes, http.MethodGet, "/api/v1/messages/nope", "", "id"},
		{routes, http.MethodPatch, "/api/v1/messages/1", `{"is_read":"yes"}`, "is_read"},
		{routes, http.MethodPatch, "/api/v1/messages/1", `{"subject":"x"}`, "subject"},
		{routes, http.MethodGet, "/api/v1/accounts/1/folders/INBOX/messages?limit=ten", "", "limit"},
		{routes, http.MethodGet, "/api/v1/accounts/0/threads/t1", "", "id"},
		{search, http.MethodGet, "/api/v1/accounts/1/search?has_attachment=1", "", "has_attachment"},
		{search, http.MethodGet, "/api/v1/accounts/1/search?after=2026-10-21", "", "after"},
		{search, http.MethodGet, "/api/v1/accounts/1/search?limit=1.5", "", "limit"},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), `"`+tc.field+`":`) {
				t.Fatalf("want 422 for %s, got %d %s", tc.field, rr.Code, rr.Body)
			}
		})
	}
	for _, tc := range []struct {
		handler      http.Handler
		method, path string
	}{
		{routes, http.MethodDelete, "/api/v1/messages/1?permanent=yes"},
		{routes, http.MethodGet, "/api/v1/accounts/1/folders/INBOX/messages?limit=500&cursor=x"},
		{search, http.MethodGet, "/api/v1/accounts/1/search?has_attachment=&after="},
	} {
		t.Run("bound "+tc.method+tc.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.handler.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("want 401, got %d %s", rr.Code, rr.Body)
			}
		})
	}
}
