package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

func TestAC03PublicStatusExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	const operation = "getPublicStatus"
	for _, name := range []string{"operational", "degraded", "internal_error"} {
		t.Run(name, func(t *testing.T) {
			pair, ok := pairs[operation][name]
			if !ok {
				t.Fatal("the named status pair is missing")
			}
			examplepairs.Proven(t, operation, name)
			rr := httptest.NewRecorder()
			switch name {
			case "internal_error":
				// This is the framework's documented fallback, shared by
				// all registered endpoints. Probe the error adapter itself.
				endpoint.WriteError(context.Background(), rr, errors.New("private unexpected failure"))
			default:
				var pingError error
				if name == "degraded" {
					pingError = errors.New("private database dependency details")
				}
				router := chi.NewRouter()
				h := handlers.NewHealthHandler(healthProbeDB(t, pingError), nil)
				h.RegisterPublicStatus(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()})
				router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
				if got := rr.Header().Get("Cache-Control"); got != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", got)
				}
			}
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("got %d %v: %s", rr.Code, rr.Header(), rr.Body.String())
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
		})
	}
}
