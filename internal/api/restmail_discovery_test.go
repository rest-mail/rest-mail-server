package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

func TestRESTMAILDiscoveryPublicRoute(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	router := NewRouter(newFailingGormDB(t), jwtSvc, &config.Config{
		Environment: "test", GatewayHostname: "mx.example.test",
	}, nil)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"", http.StatusUnprocessableEntity},
		{"?domain=a.test&domain=b.test", http.StatusUnprocessableEntity},
		{"?domain=example.test", http.StatusInternalServerError},
	} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/.well-known/restmail"+tc.query, nil))
		// No credentials: discovery must reach the handler. In particular a
		// database outage must be 500, never a 404 that could clear peer memory.
		if rr.Code != tc.status {
			t.Fatalf("GET discovery%s = %d, want %d: %s", tc.query, rr.Code, tc.status, rr.Body.String())
		}
	}
}
