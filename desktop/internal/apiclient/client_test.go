package apiclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
	keyring "github.com/zalando/go-keyring"
)

func TestTypedMailboxOperations(t *testing.T) {
	keyring.MockInit()
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/auth/login" {
			_, _ = io.WriteString(writer, `{"data":{"access_token":"typed-client-secret","refresh_token":"typed-refresh-secret","expires_in":3600,"user":{"id":42}}}`)
			return
		}
		if request.Header.Get("Authorization") != "Bearer typed-client-secret" {
			t.Error("typed client did not share the native session")
		}
		calls = append(calls, request.Method+" "+request.URL.RequestURI())
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(request.URL.Path, "/folders"):
			_, _ = io.WriteString(writer, `{"data":[{"name":"INBOX","unread":2}]}`)
		case strings.HasSuffix(request.URL.Path, "/messages"), strings.HasSuffix(request.URL.Path, "/search"):
			_, _ = io.WriteString(writer, `{"data":[{"id":7,"subject":"hello"}],"pagination":{"cursor":"next","has_more":true}}`)
		case strings.Contains(request.URL.Path, "/draft"):
			var draft Draft
			if err := json.NewDecoder(request.Body).Decode(&draft); err != nil || draft.Subject != "draft" {
				t.Error("invalid draft payload")
			}
			_, _ = io.WriteString(writer, `{"data":{"id":8,"subject":"draft"}}`)
		case request.Method == http.MethodGet:
			_, _ = io.WriteString(writer, `{"data":{"id":7,"subject":"hello"}}`)
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	manager := session.New()
	if _, err := manager.SignIn(strings.Replace(server.URL, "127.0.0.1", "localhost", 1), "admin@example.test", "password"); err != nil {
		t.Fatal(err)
	}
	client := New(manager)
	ctx := context.Background()
	folders, err := client.ListFolders(ctx, 42)
	if err != nil || len(folders) != 1 || folders[0].Unread != 2 {
		t.Fatalf("folders: %+v %v", folders, err)
	}
	messages, err := client.ListMessages(ctx, 42, "Projects/Work", Page{Cursor: "one+two", Limit: 25}, "")
	if err != nil || len(messages.Data) != 1 || messages.Pagination == nil || !messages.Pagination.HasMore {
		t.Fatalf("messages: %+v %v", messages, err)
	}
	message, err := client.GetMessage(ctx, 7)
	if err != nil || message.ID != 7 {
		t.Fatalf("get: %+v %v", message, err)
	}
	if _, err := client.Search(ctx, 42, "subject:hello world", "INBOX", Page{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListMessages(ctx, 42, "INBOX", Page{}, "flagged"); err != nil {
		t.Fatal(err)
	}
	if err := client.SendMessage(ctx, apitypes.SendMessageRequest{To: []string{"friend@example.test"}, Subject: "hello"}); err != nil {
		t.Fatal(err)
	}
	for _, draftID := range []uint{0, 8} {
		if draft, err := client.SaveDraft(ctx, draftID, Draft{Subject: "draft"}); err != nil || draft.ID != 8 {
			t.Fatalf("draft: %+v %v", draft, err)
		}
	}
	isRead := true
	if err := client.SetFlags(ctx, []uint{7, 8}, apitypes.UpdateMessageBody{IsRead: &isRead}); err != nil {
		t.Fatal(err)
	}
	if err := client.MoveMessages(ctx, []uint{7, 8}, "Archive"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteMessages(ctx, []uint{7, 8}, false); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"GET /api/v1/accounts/42/folders",
		"GET /api/v1/accounts/42/folders/Projects%2FWork/messages?cursor=one%2Btwo&limit=25",
		"GET /api/v1/messages/7",
		"GET /api/v1/accounts/42/search?folder=INBOX&q=subject%3Ahello+world",
		"GET /api/v1/accounts/42/search?folder=INBOX&q=flagged",
		"POST /api/v1/messages/send", "POST /api/v1/messages/draft", "PUT /api/v1/messages/draft/8",
		"PATCH /api/v1/messages/7", "PATCH /api/v1/messages/8", "PATCH /api/v1/messages/7", "PATCH /api/v1/messages/8",
		"DELETE /api/v1/messages/7?permanent=false", "DELETE /api/v1/messages/8?permanent=false",
	}
	if strings.Join(calls, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("wrong API routes:\n%s", strings.Join(calls, "\n"))
	}
}
