package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccountTimeZoneSetting(t *testing.T) {
	database, err := connectTestDB()
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := database.AutoMigrate(accountModels...); err != nil {
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
	path := fmt.Sprintf("/api/v1/accounts/%d/time-zone", fixture.ownerMailbox.ID)
	send := func(method, body string, caller uint) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		request := authzRequest(method, path, caller, nil, body)
		request.Header.Set("Content-Type", "application/json")
		routes.ServeHTTP(response, request)
		return response
	}
	const unset = "{\"data\":{\"time_zone\":null}}\n"
	const berlin = "{\"data\":{\"time_zone\":\"Europe/Berlin\"}}\n"
	expect := func(step string, response *httptest.ResponseRecorder, want string) {
		t.Helper()
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("%s: %d %s, want %s", step, response.Code, response.Body, want)
		}
	}
	owner := fixture.ownerMailbox.ID
	expect("default", send(http.MethodGet, "", owner), unset)
	expect("set", send(http.MethodPut, `{"time_zone":"Europe/Berlin"}`, owner), berlin)
	expect("read back", send(http.MethodGet, "", owner), berlin)

	for _, refused := range []string{`{"time_zone":"Mars/Olympus"}`, `{"time_zone":""}`, `{"time_zone":"Local"}`, `{"time_zone":"../etc/passwd"}`} {
		if response := send(http.MethodPut, refused, owner); response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s, want 422", refused, response.Code, response.Body)
		}
		expect("after "+refused, send(http.MethodGet, "", owner), berlin)
	}

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if response := send(method, `{"time_zone":"UTC"}`, fixture.attackerMailbox.ID); response.Code != http.StatusNotFound {
			t.Fatalf("%s from another account: %d %s", method, response.Code, response.Body)
		}
	}
	expect("after the other account", send(http.MethodGet, "", owner), berlin)

	expect("clear", send(http.MethodPut, `{"time_zone":null}`, owner), unset)
	expect("read cleared", send(http.MethodGet, "", owner), unset)
}
