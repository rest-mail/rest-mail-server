package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
)

func TestAccountSettingsAbsentLifecycle(t *testing.T) {
	database := openAuthzTestDB(t)
	if err := migrateSchema(database); err != nil {
		t.Fatal(err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { transaction.Rollback() })
	if err := bootstrap.EnsureRBAC(transaction); err != nil {
		t.Fatal(err)
	}
	fixture := seedAuthzFixture(t, transaction)
	grantOwnerRole(t, transaction, fixture.ownerMailbox.ID, fixture.ownerMailbox.Address)
	sieve := NewSieveHandler(transaction)
	vacation := NewVacationHandler(transaction)
	send := func(method, path, body string, want int) {
		t.Helper()
		request := authzRequest(method, path, fixture.ownerMailbox.ID, map[string]string{"id": uintToStr(fixture.ownerMailbox.ID)}, body)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		if path == "/sieve" {
			serveSieve(sieve, response, request)
		} else {
			serveVacation(vacation, response, request)
		}
		if response.Code != want {
			t.Fatalf("%s %s: got %d %s, want %d", method, path, response.Code, response.Body, want)
		}
	}
	send(http.MethodGet, "/sieve", "", 404)
	send(http.MethodDelete, "/sieve", "", 404)
	send(http.MethodPut, "/sieve", `{"script":"keep;","active":false}`, 201)
	send(http.MethodGet, "/sieve", "", 200)
	send(http.MethodDelete, "/sieve", "", 200)
	send(http.MethodGet, "/sieve", "", 404)
	send(http.MethodDelete, "/sieve", "", 404)
	send(http.MethodPut, "/sieve", `{"script":""}`, 201)
	send(http.MethodGet, "/sieve", "", 200)
	send(http.MethodDelete, "/sieve", "", 200)
	send(http.MethodDelete, "/vacation", "", 404)
	send(http.MethodPut, "/vacation", `{"enabled":true,"subject":"Away","body":"Back soon"}`, 200)
	send(http.MethodDelete, "/vacation", "", 204)
	send(http.MethodDelete, "/vacation", "", 204)
}
