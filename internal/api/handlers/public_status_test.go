package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
)

func TestPublicStatusBoundsDatabaseProbe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stalls bool
		status int
	}{
		{"database answers", false, http.StatusOK},
		{"database stalls", true, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &serviceProbeTestDB{wait: tc.stalls}
			health := NewHealthHandler(serviceProbeTestGorm(t, probe), nil)
			router := chi.NewRouter()
			health.RegisterPublicStatus(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))

			if probe.calls != 1 || !probe.deadline {
				t.Errorf("database ping calls = %d, deadline set = %v, want one bounded call", probe.calls, probe.deadline)
			}
			if response.Code != tc.status {
				t.Errorf("got HTTP %d, want %d", response.Code, tc.status)
			}
		})
	}
}
