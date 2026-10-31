package api

import (
	"encoding/json"
	"errors"
	"github.com/restmail/restmail/internal/examplepairs"
	"mime"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	yaml "go.yaml.in/yaml/v2"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/metrics"
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
	pairs, err := examplepairs.Read()
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
			examplepairs.Proven(t, id, "available")
			if !ok {
				t.Fatal("the named success pair is missing")
			}
			rr := httptest.NewRecorder()
			handler := routers.Public
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

// A failed scrape has a real sanitized error response: the production DKIM
// key-age collector cannot read a database that refuses queries. The other public
// reads above succeed on a failing database. Their fallback 500 examples describe
// the shared error adapter, rather than claiming that these handlers query SQL.
func TestPublicReadFailureExamplesMatchErrorAdapter(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	// The recovery email link page is static too: its only 500 is the shared
	// adapter's. It is not a public read of publicExamplePaths' kind.
	ids := []string{"recoveryEmailLink"}
	for id := range publicExamplePaths {
		ids = append(ids, id)
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			pair, ok := pairs[id]["internal_error"]
			examplepairs.Proven(t, id, "internal_error")
			if !ok {
				t.Fatal("the named fault pair is missing")
			}
			rr := httptest.NewRecorder()
			if id == "prometheusMetrics" {
				registry := prometheus.NewRegistry()
				registry.MustRegister(metrics.NewDKIMKeyAgeCollector(healthProbeDB(t, nil)))
				router := chi.NewRouter()
				registerMetrics(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()},
					promhttp.HandlerFor(prometheus.Gatherers{registry}, promhttp.HandlerOpts{}))
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
	t.Run("shared_error_adapter", func(t *testing.T) {
		rr := httptest.NewRecorder()
		endpoint.WriteError(rr, errors.New("dependency details must stay private"))
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("got %d, want 500", rr.Code)
		}
		assertPublicExampleBody(t, []byte(`{"error":{"code":"internal_error","message":"Internal server error"}}`), rr.Body.Bytes())
	})
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
