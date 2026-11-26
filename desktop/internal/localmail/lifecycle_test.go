package localmail

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"testing"

	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	"github.com/rest-mail/rest-mail-server/desktop/internal/session"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

func TestEventMessageIDsAndSessionBoundaries(t *testing.T) {
	for _, check := range []struct {
		data map[string]any
		want uint
	}{
		{map[string]any{"message_id": float64(123456789)}, 123456789},
		{map[string]any{"id": "123456789"}, 123456789},
		{map[string]any{"id": uint(42)}, 42},
		{map[string]any{"id": -1}, 0},
		{map[string]any{"id": 1.5}, 0},
		{map[string]any{"id": math.NaN()}, 0},
		{map[string]any{"id": float64(1 << 53)}, 0},
		{nil, 0},
	} {
		if got := eventMessageID(check.data); got != check.want {
			t.Fatalf("event %v: got %d want %d", check.data, got, check.want)
		}
	}
	if sameSession(session.SessionStatus{AccountID: 1}, session.SessionStatus{AccountID: 2}) || sameSession(session.SessionStatus{SetupRequired: []string{"totp"}}, session.SessionStatus{}) {
		t.Fatal("distinct sessions conflated")
	}
}

func TestObservedCatalogFoldersMessagesOfflineFallbackAndReindex(t *testing.T) {
	backend := newBackend(t)
	backend.messages[1] = testMessage(1)
	service := newService(t, backend)
	ctx := t.Context()
	waitFor(t, func() bool { status, err := service.IndexStatus(ctx, 1); return err == nil && status.Bodies == 1 })
	if service.Root() != service.root || service.String() == "" {
		t.Fatal("missing service identity")
	}
	response, err := service.Do(ctx, "GET", "/api/v1/accounts", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	backend.mu.Lock()
	backend.offline = true
	backend.mu.Unlock()
	for _, check := range []struct {
		parts []string
		data  any
	}{
		{[]string{"accounts", "1", "folders"}, map[string]any{"data": []apitypes.FolderSummary{{Folder: "INBOX"}, {Folder: "Archive"}}}},
		{[]string{"accounts", "1", "folders", "INBOX", "messages"}, apiclient.MessagePage{Data: []apitypes.Message{testMessage(2)}}},
		{[]string{"messages", "2"}, map[string]any{"data": testMessage(2)}},
		{[]string{"unrelated"}, map[string]any{"ok": true}},
	} {
		response, err = responseJSON(check.data, "server", false)
		if err != nil {
			t.Fatal(err)
		}
		response, err = service.observe(ctx, check.parts, response, backend.Status())
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	response, err = service.Do(ctx, "GET", "/api/v1/accounts", nil)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Data    []apitypes.AccountResponse `json:"data"`
		Offline bool                       `json:"offline"`
	}
	err = json.NewDecoder(response.Body).Decode(&catalog)
	response.Body.Close()
	if err != nil || len(catalog.Data) != 1 || !catalog.Offline || response.Header.Get("X-Restmail-Source") != "local" {
		t.Fatalf("offline catalog: %+v %v", catalog, err)
	}
	if err = service.Reindex(ctx, 1); err != nil {
		t.Fatal(err)
	}
	result, err := service.LocalSearch(ctx, 1, "cafe", "")
	if err != nil || len(result.Data) != 2 || result.Complete {
		t.Fatalf("offline reindex: %+v %v", result, err)
	}
	response, err = service.Do(ctx, "GET", "/api/v1/messages/2", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	request, _ := http.NewRequest("PATCH", "/api/v1/accounts", nil)
	if _, err = service.fallback(ctx, request, []string{"accounts"}, errors.New("offline")); err == nil {
		t.Fatal("offline write accepted")
	}
	runCtx, stop := context.WithCancel(ctx)
	stop()
	service.Run(runCtx)
	if err = service.ForgetAll(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(service.root)
	if err != nil || len(files) != 0 {
		t.Fatalf("account files remain: %v %v", files, err)
	}
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}
	if err = service.Activate(); err == nil {
		t.Fatal("closed service activated")
	}
	if _, err = service.IndexStatus(ctx, 1); err == nil {
		t.Fatal("closed service status accepted")
	}
}

func TestObserveRejectsStaleSession(t *testing.T) {
	backend := newBackend(t)
	service := newService(t, backend)
	response, err := responseJSON(map[string]any{"data": []apitypes.AccountResponse{}}, "server", false)
	if err != nil {
		t.Fatal(err)
	}
	identity := backend.Status()
	identity.Email = "other@test"
	if _, err = service.observe(t.Context(), []string{"accounts"}, response, identity); !errors.Is(err, session.ErrSignedOut) {
		t.Fatalf("stale observation: %v", err)
	}
	if _, err = responseJSON(math.Inf(1), "server", false); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err = service.LocalSearch(t.Context(), 999, "hello", ""); err == nil {
		t.Fatal("unopened account accepted")
	}
	if _, err = service.LocalSearch(t.Context(), 1, "hello", ""); err != nil {
		t.Fatal(err)
	}
}
