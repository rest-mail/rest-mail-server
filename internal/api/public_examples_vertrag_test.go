package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This optional boundary check uses the real Vertrag executable against the
// production registered handlers. The ordinary route tests cover the text/YAML
// documents and the isolated metrics collector without an external executable.
func TestPublicJSONExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to check the public JSON examples with Vertrag")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers := describedRouters(t)
	doc := contractDocument(t, routers.generator("public examples"))
	paths := doc["paths"].(map[string]interface{})
	// Select the actual public JSON operations without changing their response schemas
	// or examples. Fault responses are proved by the separate adapter test.
	selected := map[string]interface{}{}
	for _, path := range []string{"/api/docs/openapi.json", "/restmail/capabilities", "/api", "/api/v1/server"} {
		selected[path] = paths[path]
	}
	doc["paths"] = selected
	document, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"none", "wrong status", "wrong JSON shape"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			spec := filepath.Join(dir, "openapi.json")
			if err := os.WriteFile(spec, document, 0600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/docs/openapi.json" && fault != "none" {
					actual := httptest.NewRecorder()
					routers.Public.ServeHTTP(actual, r)
					for name, values := range actual.Header() {
						w.Header()[name] = values
					}
					if fault == "wrong status" {
						w.WriteHeader(http.StatusAccepted)
						_, _ = w.Write(actual.Body.Bytes())
					} else {
						_, _ = w.Write([]byte(`"a string is not an OpenAPI document"`))
					}
					return
				}
				routers.Public.ServeHTTP(w, r)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			reportPath := filepath.Join(dir, "report.json")
			cmd := exec.CommandContext(ctx, binary, "run", "--endpoint", server.URL,
				"--only-matching", " > 200", "--reporter", "json", "--output", reportPath, spec)
			cmd.Dir = dir // Repository hooks and phase configuration cannot alter this check.
			output, runErr := cmd.CombinedOutput()
			if fault == "none" && runErr != nil {
				t.Fatalf("Vertrag rejected the actual handlers: %v\n%s", runErr, output)
			}
			if fault != "none" {
				exit, ok := runErr.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 {
					t.Fatalf("Vertrag did not reject %s: %v\n%s", fault, runErr, output)
				}
			}
			data, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatalf("no Vertrag report: %v\n%s", err, output)
			}
			var report struct {
				Summary struct{ Total, Passing, Failing, Errors, Skipped int }
			}
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			passing := 4
			if fault != "none" {
				passing = 3
			}
			if report.Summary.Total != 4 || report.Summary.Passing != passing || report.Summary.Failing != 4-passing || report.Summary.Errors != 0 || report.Summary.Skipped != 0 {
				t.Fatalf("the selected public transactions were not all exercised: %s", data)
			}
		})
	}
}
