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

func TestDomainCleanupExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to replay domain cleanup examples")
	}
	useRealGrants(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	full := contractDocument(t, describedRouters(t).generator("forward attachments examples"))
	snapshot, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range domainCleanupScenarios {
		t.Run(tc.name, func(t *testing.T) {
			pair, ok := pairs["deleteDomain"][tc.name]
			if !ok {
				t.Fatal("canonical domain cleanup pair missing")
			}
			f := newDomainCleanupFixture(t, tc.mode)
			token := domainCleanupToken(f, tc.mode)
			if tc.mode == "missing" {
				delete(f.c.ledger.domains, 7)
			}
			var doc map[string]any
			if err := json.Unmarshal(snapshot, &doc); err != nil {
				t.Fatal(err)
			}
			const path = "/api/v1/admin/domains/{id}"
			op := doc["paths"].(map[string]any)[path].(map[string]any)["delete"].(map[string]any)
			doc["paths"] = map[string]any{path: map[string]any{"delete": op}}
			status := strconv.Itoa(pair.Response.Status)
			response := op["responses"].(map[string]any)[status].(map[string]any)
			content, _ := response["content"].(map[string]any)
			for _, value := range content {
				media := value.(map[string]any)
				selected := media["examples"].(map[string]any)[tc.name]
				if selected == nil {
					t.Fatal("response example missing")
				}
				media["examples"] = map[string]any{tc.name: selected}
			}
			op["responses"] = map[string]any{status: response}
			for _, value := range op["parameters"].([]any) {
				parameter := value.(map[string]any)
				delete(parameter, "examples")
				parameter["example"] = 7
			}
			dir := t.TempDir()
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "openapi.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			hook := "import vertrag_hooks as hooks\n@hooks.before_each\ndef exact_cleanup_pair(transaction):\n    transaction['fullPath'] = '/api/v1/admin/domains/7'\n"
			if err := os.WriteFile(filepath.Join(dir, "pair.py"), []byte(hook), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte("language: python\nhookfiles: ./pair.py\n"), 0600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				actual := httptest.NewRecorder()
				f.router.ServeHTTP(actual, r)
				assertDomainCleanupResponse(t, f, actual.Code)
				normalized := actual.Body.Bytes()
				for name, values := range actual.Header() {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				w.WriteHeader(actual.Code)
				_, _ = w.Write(normalized)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			args := []string{"run", "--endpoint", server.URL, "--reporter", "json", "--output", "report.json"}
			if token != "" {
				args = append(args, "--header", "Authorization: Bearer "+token)
			}
			args = append(args, "openapi.json")
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			if err != nil {
				report, _ := os.ReadFile(filepath.Join(dir, "report.json"))
				t.Fatalf("domain cleanup exchange failed: %v\n%s\n%s", err, output, report)
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
				t.Fatalf("domain cleanup exchange did not execute and pass: %s", data)
			}
		})
	}
}
