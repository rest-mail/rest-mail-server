package session

import (
	"context"
	"net/http"
	"testing"
)

func TestBoundImportNeverUploadsToAnotherServer(t *testing.T) {
	fixture := newAuthFixture(t, false)
	manager := New()
	signIn(t, manager, fixture)
	ctx := BindServer(context.Background(), fixture.server)
	response, err := manager.Do(ctx, http.MethodGet, "/api/v1/accounts", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	manager.mu.Lock()
	manager.current.Server = "https://different.example.test"
	manager.mu.Unlock()
	if response, err := manager.Do(ctx, http.MethodPost, "/api/v1/accounts/7/messages/import", nil); err == nil {
		response.Body.Close()
		t.Fatal("bound request followed a different login")
	}
}
