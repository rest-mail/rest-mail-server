package api

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	yaml "go.yaml.in/yaml/v2"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/version"
)

var publicExamplePaths = map[string]string{
	"apiRoot":              "/api",
	"describeServer":       "/api/v1/server",
	"openapiDocument":      "/api/docs/openapi.json",
	"openapiSpec":          "/api/docs/openapi.yaml",
	"prometheusMetrics":    "/metrics",
	"restmailCapabilities": "/restmail/capabilities",
	"swaggerUI":            "/api/docs",
}

// These examples describe real registered public GETs. Documents and changing
// counters are checked structurally, rather than frozen into recursive snapshots
// of the description that embeds the examples themselves (#1015, #1016, #1021,
// #1036, #1058).
func TestPublicReadExamplesMatchRegisteredResponses(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	routers := describedRouters(t)
	metrics := chi.NewRouter()
	rs := endpoint.Routes{Router: metrics, Registry: endpoint.NewRegistry()}
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "restmail_example_ready", Help: "The example scrape is ready."})
	gauge.Set(1)
	registry.MustRegister(gauge)
	registerMetrics(rs, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	for id, path := range publicExamplePaths {
		t.Run(id, func(t *testing.T) {
			pair, ok := pairs[id]["available"]
			if !ok {
				t.Fatal("the named success pair is missing")
			}
			rr := httptest.NewRecorder()
			var handler http.Handler = routers.Public
			if id == "prometheusMetrics" {
				handler = metrics // the production registration, with an isolated collector
			}
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
			media, _, err := mime.ParseMediaType(rr.Header().Get("Content-Type"))
			if err != nil || rr.Code != pair.Response.Status || media != pair.Response.MediaType {
				t.Fatalf("got %d %q, want %d %q: %v", rr.Code, rr.Header().Get("Content-Type"), pair.Response.Status, pair.Response.MediaType, err)
			}
			switch id {
			case "openapiDocument":
				var doc openAPIDescription
				if err := endpoint.BindBody(&doc, rr.Body.Bytes()); err != nil {
					t.Fatalf("served JSON does not fit its registered document type: %v", err)
				}
				if doc.OpenAPI != "3.1.0" || doc.Info["title"] != "REST Mail API" || doc.Paths[path] == nil {
					t.Fatalf("not the generated OpenAPI document: %+v", doc)
				}
			case "openapiSpec":
				var doc map[string]interface{}
				if err := yaml.Unmarshal(rr.Body.Bytes(), &doc); err != nil || doc["openapi"] != "3.1.0" || doc["paths"] == nil {
					t.Fatalf("not an OpenAPI YAML document: %v", err)
				}
			case "swaggerUI":
				if !strings.HasPrefix(rr.Body.String(), "<!DOCTYPE html>") || !strings.Contains(rr.Body.String(), "SwaggerUIBundle({") || !strings.Contains(rr.Body.String(), "/api/docs/openapi.yaml") {
					t.Fatal("the HTML does not load the served description")
				}
			case "prometheusMetrics":
				if !strings.Contains(rr.Body.String(), "restmail_example_ready 1\n") {
					t.Fatalf("not the collector's exposition: %s", rr.Body.String())
				}
			case "apiRoot", "describeServer":
				var expected map[string]map[string]interface{}
				if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
					t.Fatal(err)
				}
				expected["data"]["version"] = version.Version
				if id == "apiRoot" {
					expected["data"]["commit"] = version.Commit
					expected["data"]["build_date"] = version.BuildDate
				}
				body, err := json.Marshal(expected)
				if err != nil {
					t.Fatal(err)
				}
				assertPublicExampleBody(t, body, rr.Body.Bytes())
			case "restmailCapabilities":
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			}
		})
	}
	for _, warning := range routers.generator("examples").Report() {
		if _, selected := publicExamplePaths[warning.OperationID]; selected {
			t.Errorf("public operation still has a generator warning: %v", warning)
		}
	}
}

// A healthy request cannot make these read-only handlers fail on demand. Their
// named 500 bodies are proved through the shared production error adapter; the
// metrics case also reaches it through a real failed scrape. No fault switch is
// exposed by the production API.
func TestPublicReadFailureExamplesMatchErrorAdapter(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for id := range publicExamplePaths {
		t.Run(id, func(t *testing.T) {
			pair, ok := pairs[id]["internal_error"]
			if !ok {
				t.Fatal("the named fault pair is missing")
			}
			rr := httptest.NewRecorder()
			if id == "prometheusMetrics" {
				router := chi.NewRouter()
				registerMetrics(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "collector dependency details must stay private", http.StatusServiceUnavailable)
				}))
				router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			} else {
				endpoint.WriteError(rr, errors.New("dependency details must stay private"))
			}
			if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("got %d %v", rr.Code, rr.Header())
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
		})
	}
}

func assertPublicExampleBody(t *testing.T, expected, actual []byte) {
	t.Helper()
	var want, got interface{}
	if err := json.Unmarshal(expected, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %s, want %s", actual, expected)
	}
}
