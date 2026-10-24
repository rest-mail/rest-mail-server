package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	acmeclient "github.com/restmail/restmail/internal/acme"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

func TestRegisteredACMEPreservesTheRespondersAnswers(t *testing.T) {
	responder := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/acme-challenge/known" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("known.key-authorization"))
	})
	router := chi.NewRouter()
	registerACME(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, responder)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, token := range []string{"known", "missing"} {
			t.Run(method+"/"+token, func(t *testing.T) {
				request := httptest.NewRequest(method, "/.well-known/acme-challenge/"+token, nil)
				before, after := httptest.NewRecorder(), httptest.NewRecorder()
				responder.ServeHTTP(before, request)
				router.ServeHTTP(after, request)
				if before.Code != after.Code || before.Body.String() != after.Body.String() || !reflect.DeepEqual(before.Header(), after.Header()) {
					t.Fatalf("registered answer %d %v %q differs from responder %d %v %q",
						after.Code, after.Header(), after.Body.String(), before.Code, before.Header(), before.Body.String())
				}
			})
		}
	}
}

func TestACMEConfiguredRoutesAreRegisteredAndDescribed(t *testing.T) {
	jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	client := acmeclient.NewClient(acmeclient.ClientConfig{})
	routers := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: "test"}, nil, client)
	t.Cleanup(routers.Close)
	if err := chi.Walk(routers.Public.(chi.Routes), func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !routers.Registry.Has(method, path) {
			t.Errorf("ACME-enabled server serves %s %s without registering it", method, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	description, warnings := routers.Description("test")
	if len(warnings) > 0 {
		t.Fatal(warnings)
	}
	data, err := json.Marshal(description)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			OperationID string        `json:"operationId"`
			Security    []interface{} `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	operations := document.Paths["/.well-known/acme-challenge/{token}"]
	if operations["get"].OperationID != "acmeChallenge" || operations["head"].OperationID != "headAcmeChallenge" || len(operations["get"].Security) != 0 {
		t.Fatalf("ACME description: %+v", operations)
	}
	recorder := httptest.NewRecorder()
	routers.Public.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/missing", nil))
	if recorder.Code != http.StatusNotFound || recorder.Body.String() != "404 page not found\n" {
		t.Fatalf("unissued challenge: %d %q", recorder.Code, recorder.Body.String())
	}
}
