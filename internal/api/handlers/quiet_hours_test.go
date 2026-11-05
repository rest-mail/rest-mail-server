package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuietHoursAccountSetting(t *testing.T) {
	database, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateModels(database, accountModels...); err != nil {
		t.Fatal(err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedAuthzFixture(t, tx)
	ensureDefaultServerCeiling(t, tx)
	routes := contactRoutes(NewContactHandler(tx))
	path := fmt.Sprintf("/api/v1/accounts/%d/quiet-hours", fixture.ownerMailbox.ID)
	send := func(method, body string, caller uint) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		request := authzRequest(method, path, caller, nil, body)
		request.Header.Set("Content-Type", "application/json")
		routes.ServeHTTP(response, request)
		return response
	}
	if response := send(http.MethodGet, "", fixture.ownerMailbox.ID); response.Code != 200 || response.Body.String() != "{\"data\":{\"enabled\":false,\"start\":\"\",\"end\":\"\",\"time_zone\":\"\"}}\n" {
		t.Fatalf("default: %d %s", response.Code, response.Body)
	}
	setting := `{"enabled":true,"start":"22:00","end":"08:00","time_zone":"Europe/Berlin"}`
	for _, method := range []string{http.MethodPut, http.MethodGet} {
		if response := send(method, setting, fixture.ownerMailbox.ID); response.Code != 200 || !strings.Contains(response.Body.String(), setting) {
			t.Fatalf("persisted setting: %d %s", response.Code, response.Body)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if response := send(method, setting, fixture.attackerMailbox.ID); response.Code != http.StatusNotFound {
			t.Fatalf("cross-account access: %d %s", response.Code, response.Body)
		}
	}
	if response := send(http.MethodPut, strings.Replace(setting, "Europe/Berlin", "not/a/zone", 1), fixture.ownerMailbox.ID); response.Code != 422 || !strings.Contains(response.Body.String(), "time_zone") {
		t.Fatalf("invalid zone: %d %s", response.Code, response.Body)
	}
	if response := send(http.MethodGet, "", fixture.ownerMailbox.ID); response.Code != 200 || !strings.Contains(response.Body.String(), setting) {
		t.Fatalf("invalid update changed setting: %d %s", response.Code, response.Body)
	}
	if response := send(http.MethodPut, strings.Replace(setting, "true", "false", 1), fixture.ownerMailbox.ID); response.Code != 200 || !strings.Contains(response.Body.String(), `"enabled":false`) {
		t.Fatalf("disable: %d %s", response.Code, response.Body)
	}
}
