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
	"strings"
	"testing"
	"time"
)

// namedPairExchange is one published example pair, and the exact request that
// sends it. The body is raw because a refused request's body may not be JSON.
type namedPairExchange struct {
	method, path, uri, name string
	status                  int
	body, token             string
}

// checkNamedPairWithVertrag sends one published pair through Vertrag, against
// the description of its operation narrowed to that pair. A hook supplies the
// path and the raw body, so invalid values reach the server exactly as written;
// the described constraints and the expected response are not changed.
func checkNamedPairWithVertrag(t *testing.T, binary string, handler http.Handler, exchange namedPairExchange) {
	t.Helper()
	doc := contractDocument(t, describedRouters(t).pairGenerator(t, "named pair examples"))
	op := doc["paths"].(map[string]any)[exchange.path].(map[string]any)[strings.ToLower(exchange.method)].(map[string]any)
	doc["paths"] = map[string]any{exchange.path: map[string]any{strings.ToLower(exchange.method): op}}
	status := strconv.Itoa(exchange.status)
	response := op["responses"].(map[string]any)[status].(map[string]any)
	for _, v := range response["content"].(map[string]any) {
		media := v.(map[string]any)
		selected := media["examples"].(map[string]any)[exchange.name]
		if selected == nil {
			t.Fatal("response example missing")
		}
		media["examples"] = map[string]any{exchange.name: selected}
	}
	op["responses"] = map[string]any{status: response}
	if request, ok := op["requestBody"].(map[string]any); ok {
		for _, v := range request["content"].(map[string]any) {
			media := v.(map[string]any)
			if examples, ok := media["examples"].(map[string]any); ok {
				media["examples"] = map[string]any{exchange.name: examples[exchange.name]}
			}
		}
	}
	// The hook sends the exact published pair, including invalid path/body
	// examples. Compiler placeholders never change the described constraints.
	if params, ok := op["parameters"].([]any); ok {
		for _, v := range params {
			p := v.(map[string]any)
			delete(p, "examples")
			p["example"] = 12
		}
	}
	dir := t.TempDir()
	data, err := marshalPairDescription(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	hook := "import vertrag_hooks as hooks\n@hooks.before_each\ndef exact_pair(transaction):\n    transaction['fullPath'] = " + strconv.Quote(exchange.uri) + "\n    transaction['request']['body'] = " + strconv.Quote(exchange.body) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "pair.py"), []byte(hook), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte("language: python\nhookfiles: ./pair.py\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	args := []string{"run", "--endpoint", server.URL, "--reporter", "json", "--output", "report.json"}
	if exchange.token != "" {
		args = append(args, "--header", "Authorization: Bearer "+exchange.token)
	}
	args = append(args, "openapi.json")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("named pair exchange failed: %v\n%s", err, output)
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
		t.Fatalf("named pair exchange did not execute and pass: %s", data)
	}
}
