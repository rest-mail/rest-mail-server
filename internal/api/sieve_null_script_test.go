package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// A null Sieve script is refused like a missing one, so the served description says script
// is a string and never null, on both the install and the validate body (#1163).
func TestSieveBodiesDescribeTheScriptAsNeverNull(t *testing.T) {
	recorder := httptest.NewRecorder()
	describedRouters(t).Public.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/docs/openapi.json", nil))
	var served struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string                   `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"putSieveBody", "validateSieveBody"} {
		body, ok := served.Components.Schemas[name]
		if !ok {
			t.Fatalf("%s is not described", name)
		}
		if !slices.Contains(body.Required, "script") {
			t.Errorf("%s required is %v, want script", name, body.Required)
		}
		if got := string(body.Properties["script"]); got != `{"type":"string"}` {
			t.Errorf("%s script is %s, want a string that is never null", name, got)
		}
	}
}

// The binder refuses a null script exactly where it already refused a missing one: after
// authentication, and before the handler's capability and account checks, with the same answer.
// Before #1163 a null script got past the binder and the handler refused it after those checks,
// so a key without mail:write, or another account, saw 403 or 404 for null and 422 for missing.
func TestSieveNullScriptIsRefusedLikeAMissingOne(t *testing.T) {
	routes := []struct {
		name, method, path string
		scenarios          mailRemovalScenarios
	}{
		{"put", http.MethodPut, "/api/v1/accounts/1/sieve", newSieveInstallationScenarios(t)},
		{"validate", http.MethodPost, "/api/v1/accounts/1/sieve/validate", newSieveMutationScenarios(t)},
	}
	const required = "{\"error\":{\"code\":\"validation_failed\",\"message\":\"Request body failed validation\",\"details\":{\"fields\":{\"script\":\"required\"}}}}\n"
	for _, route := range routes {
		for _, scenario := range []string{"unauthenticated", "key_read", "no_grants", "found"} {
			answers := map[string]string{}
			for _, body := range []string{`{}`, `{"script":null}`} {
				t.Run(route.name+"/"+scenario+"/"+body, func(t *testing.T) {
					s, ok := route.scenarios.scenarios[scenario]
					if !ok {
						t.Fatalf("no %s scenario", scenario)
					}
					before := len(route.scenarios.store.snapshot())
					req := httptest.NewRequest(route.method, route.path, strings.NewReader(body))
					if s.token != "" {
						req.Header.Set("Authorization", "Bearer "+s.token)
					}
					req.Header.Set("Content-Type", "application/json")
					rr := httptest.NewRecorder()
					s.handler.ServeHTTP(rr, req)
					want, answer := http.StatusUnprocessableEntity, required
					if scenario == "unauthenticated" {
						want, answer = http.StatusUnauthorized, ""
					}
					if rr.Code != want || (answer != "" && rr.Body.String() != answer) {
						t.Fatalf("got %d %s, want %d %s", rr.Code, rr.Body.String(), want, answer)
					}
					answers[body] = rr.Body.String()
					if len(route.scenarios.store.snapshot()) != before {
						t.Fatal("a refused script reached Sieve storage")
					}
				})
			}
			if answers[`{}`] != answers[`{"script":null}`] {
				t.Errorf("%s %s: missing answered %s, null %s", route.name, scenario, answers[`{}`], answers[`{"script":null}`])
			}
		}
	}
}
