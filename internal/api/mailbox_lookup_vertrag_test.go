package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

// Vertrag 0.30 rejects explicit empty required query examples before hooks run.
// Retain the registered schema and response example, give the compiler a
// nonempty illustration, then send the canonical pair's exact URI through an
// ordinary hook. No expected response or schema is rewritten. The hook also
// supplies genuinely omitted query parameters and oversized transport bodies.
func checkMailboxLookupWithVertrag(t *testing.T, binary string, handler http.Handler, token, path, name string, pair oapifly.ExamplePair) {
	t.Helper()
	doc := contractDocument(t, describedRouters(t).generator("mailbox lookup boundary"))
	op := doc["paths"].(map[string]interface{})[path].(map[string]interface{})["get"].(map[string]interface{})
	doc["paths"] = map[string]interface{}{path: map[string]interface{}{"get": op}}
	status := strconv.Itoa(pair.Response.Status)
	response := op["responses"].(map[string]interface{})[status].(map[string]interface{})
	for _, value := range response["content"].(map[string]interface{}) {
		media := value.(map[string]interface{})
		examples := media["examples"].(map[string]interface{})
		chosen, ok := examples[name]
		if !ok {
			t.Fatalf("published response %s is missing", name)
		}
		media["examples"] = map[string]interface{}{name: chosen}
	}
	op["responses"] = map[string]interface{}{status: response}
	uri := path
	for key, value := range pair.Request.Path {
		text := fmt.Sprint(value)
		if numeric, ok := value.(float64); ok {
			text = strconv.FormatFloat(numeric, 'f', -1, 64)
		}
		uri = strings.ReplaceAll(uri, "{"+key+"}", text)
	}
	query := url.Values{}
	for key, value := range pair.Request.Query {
		query.Set(key, fmt.Sprint(value))
	}
	if len(query) > 0 {
		uri += "?" + query.Encode()
	}
	if parameters, ok := op["parameters"].([]interface{}); ok {
		for _, item := range parameters {
			parameter := item.(map[string]interface{})
			delete(parameter, "examples")
			if parameter["in"] == "query" {
				parameter["example"] = "compiler@example.com"
			} else {
				parameter["example"] = 1
			}
		}
	}
	dir := t.TempDir()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	hook := "import vertrag_hooks as hooks\n@hooks.before_each\ndef published_request(transaction):\n    transaction['fullPath'] = " + strconv.Quote(uri) + "\n"
	if name == "request_too_large" {
		hook += "    transaction['request']['body'] = 'oversized'\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "pair.py"), []byte(hook), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte("language: python\nhookfiles: ./pair.py\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	args := []string{"run", "--endpoint", server.URL, "--reporter", "json", "--output", "report.json"}
	if token != "" {
		args = append(args, "--header", "Authorization: Bearer "+token)
	}
	args = append(args, "openapi.json")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Vertrag rejected registered exchange: %v\n%s", err, output)
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
		t.Fatalf("exchange did not run and pass: %s", data)
	}
}
