package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccountSettingsAbsent(t *testing.T) {
	reads := accountReadScenarios(t)
	sieve := newSieveMutationScenarios(t)
	vacation := newVacationMutationScenarios(t)
	for _, scenario := range []struct {
		name, method, path, message string
		principal                   accountReadScenario
	}{
		{"get_sieve", http.MethodGet, "/api/v1/accounts/2/sieve", "Sieve script not found", reads["defaults"]},
		{"delete_sieve", http.MethodDelete, "/api/v1/accounts/2/sieve", "Sieve script not found", sieve.scenarios["missing"]},
		{"disable_vacation", http.MethodDelete, "/api/v1/accounts/2/vacation", "Vacation reply not found", vacation.scenarios["missing"]},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			request := httptest.NewRequest(scenario.method, scenario.path, nil)
			request.Header.Set("Authorization", "Bearer "+scenario.principal.token)
			response := httptest.NewRecorder()
			scenario.principal.handler.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("got %d %s, want 404", response.Code, response.Body.String())
			}
			assertPublicExampleBody(t, []byte(`{"error":{"code":"not_found","message":"`+scenario.message+`"}}`), response.Body.Bytes())
		})
	}
}
