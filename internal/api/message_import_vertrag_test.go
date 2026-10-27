package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func TestMessageImportExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to replay import examples")
	}
	useRealGrants(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	full := contractDocument(t, describedRouters(t).generator("import examples"))
	snapshot, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range importScenarios {
		t.Run(tc.name, func(t *testing.T) {
			pair, ok := pairs["importMessage"][tc.name]
			if !ok {
				t.Fatal("canonical import pair missing")
			}
			f := newImportFixture(t, tc.mode)
			prepareImportScenario(t, tc, f)
			var doc map[string]any
			if err := json.Unmarshal(snapshot, &doc); err != nil {
				t.Fatal(err)
			}
			const path = "/api/v1/accounts/{id}/messages/import"
			op := doc["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
			doc["paths"] = map[string]any{path: map[string]any{"post": op}}
			status := strconv.Itoa(pair.Response.Status)
			response := op["responses"].(map[string]any)[status].(map[string]any)
			for _, value := range response["content"].(map[string]any) {
				media := value.(map[string]any)
				selected := media["examples"].(map[string]any)[tc.name]
				if selected == nil {
					t.Fatal("response example missing")
				}
				media["examples"] = map[string]any{tc.name: selected}
			}
			op["responses"] = map[string]any{status: response}
			for _, value := range op["requestBody"].(map[string]any)["content"].(map[string]any) {
				media := value.(map[string]any)
				media["examples"] = map[string]any{tc.name: media["examples"].(map[string]any)[tc.name]}
			}
			for _, value := range op["parameters"].([]any) {
				parameter := value.(map[string]any)
				delete(parameter, "examples")
				parameter["example"] = 12
			}
			dir := t.TempDir()
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "openapi.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			id := string(mustImportJSON(t, pair.Request.Path["id"]))
			hook := "import vertrag_hooks as hooks\n@hooks.before_each\ndef exact_import_pair(transaction):\n    transaction['fullPath'] = " + strconv.Quote("/api/v1/accounts/"+id+"/messages/import") + "\n    transaction['request']['body'] = " + strconv.Quote(string(pair.Request.Body)) + "\n"
			if err := os.WriteFile(filepath.Join(dir, "pair.py"), []byte(hook), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte("language: python\nhookfiles: ./pair.py\n"), 0600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authorizeImportScenario(tc, f, r)
				f.router.ServeHTTP(w, r)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "run", "--endpoint", server.URL, "--reporter", "json", "--output", "report.json", "openapi.json")
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("import exchange failed: %v\n%s", err, output)
			}
			data, err = os.ReadFile(filepath.Join(dir, "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				Summary struct{ Total, Passing, Failing, Errors, Skipped int }
			}
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			if report.Summary.Total != 1 || report.Summary.Passing != 1 || report.Summary.Failing != 0 || report.Summary.Errors != 0 || report.Summary.Skipped != 0 {
				t.Fatalf("import exchange did not execute and pass: %s", data)
			}
		})
	}
}
