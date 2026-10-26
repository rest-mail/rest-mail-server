package api

import (
	"bytes"
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

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/pipeline"
)

const customFilterTestingPath = "/api/v1/admin/custom-filters/{id}/test"

func TestCustomFilterTestingExamples(t *testing.T) {
	f, calls := newCustomFilterTestingScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["testCustomFilter"]) < 35 {
		t.Fatal("execution examples incomplete")
	}
	for name, pair := range pairs["testCustomFilter"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			body := []byte(pair.Request.Body)
			if name == "empty_body" {
				body = nil
			}
			if name == "malformed_json" {
				body = []byte("{")
			}
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			if name == "body_boundary" {
				body = append(body, bytes.Repeat([]byte(" "), 512-len(body))...)
			}
			before := len(calls.snapshot())
			dbBefore := 0
			if s.store != nil {
				dbBefore = len(s.store.snapshot())
			}
			rr := mailboxSessionEndingRequest(t, s, http.MethodPost, messageCollectionReadURI(customFilterTestingPath, pair), body)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			after := calls.snapshot()[before:]
			expected := rr.Code == 200 || strings.HasPrefix(name, "sidecar_")
			if (len(after) == 1) != expected || len(after) > 1 {
				t.Fatalf("sidecar calls%d want%t", len(after), expected)
			}
			if len(after) == 1 {
				var request map[string]interface{}
				_ = json.Unmarshal(pair.Request.Body, &request)
				email := request["email"]
				if email == nil {
					raw, _ := json.Marshal(pipeline.EmailJSON{Envelope: pipeline.Envelope{MailFrom: "test@example.com", RcptTo: []string{"user@example.com"}, Direction: "inbound"}, Headers: pipeline.Headers{From: []pipeline.Address{{Address: "test@example.com"}}, To: []pipeline.Address{{Address: "user@example.com"}}, Subject: "Test email"}, Body: pipeline.Body{ContentType: "text/plain", Content: "This is a test email for filter testing."}})
					_ = json.Unmarshal(raw, &email)
				}
				id := int64(1)
				if v, ok := pair.Request.Path["id"]; ok {
					b, _ := json.Marshal(v)
					_ = json.Unmarshal(b, &id)
				}
				script := map[int64]string{1: "normal", 5: "syntax-error", 6: "execution-timeout", 7: "too-large", 8: "worker-error", 9: "token-refused", 10: "invalid-json", 11: "truncated-response", 12: "transport-unavailable", 13: "spaced-response"}[id]
				want, _ := json.Marshal(map[string]interface{}{"script": script, "email": email})
				got, _ := json.Marshal(after[0])
				if !bytes.Equal(want, got) {
					t.Fatalf("sidecar payload%s want%s", got, want)
				}
			}
			if s.store != nil {
				for _, c := range s.store.snapshot()[dbBefore:] {
					if c.query == "EXECUTE_WRITE" {
						t.Fatal("preview persisted a resource")
					}
					if c.query == "EXECUTE_CONTEXT" && (len(c.args) != 1 || c.args[0].Value != true) {
						t.Fatal("execution lookup lost HTTP context")
					}
				}
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "testCustomFilter" {
				t.Errorf("execution warning:%v", w)
			}
		}
	}
}

func TestCustomFilterTestingExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newCustomFilterTestingScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["testCustomFilter"] {
		t.Run(name, func(t *testing.T) {
			checkCustomFilterTestingPairWithVertrag(t, binary, f[name], http.MethodPost, customFilterTestingPath, name, pair, 513)
		})
	}
}

func checkCustomFilterTestingPairWithVertrag(t *testing.T, binary string, s bulkQueueExampleScenario, method, path, name string, pair oapifly.ExamplePair, oversizedBytes ...int) {
	t.Helper()
	doc := contractDocument(t, describedRouters(t).generator("bulk queue boundary"))
	verb := strings.ToLower(method)
	op := doc["paths"].(map[string]interface{})[path].(map[string]interface{})[verb].(map[string]interface{})
	doc["paths"] = map[string]interface{}{path: map[string]interface{}{verb: op}}
	if body, ok := op["requestBody"].(map[string]interface{}); ok {
		media := body["content"].(map[string]interface{})["application/json"].(map[string]interface{})
		examples := media["examples"].(map[string]interface{})
		selected, ok := examples[name]
		if !ok {
			t.Fatalf("missing published request %s", name)
		}
		media["examples"] = map[string]interface{}{name: selected}
	}
	uri := messageCollectionReadURI(path, pair)
	if parameters, ok := op["parameters"].([]interface{}); ok {
		for _, value := range parameters {
			parameter := value.(map[string]interface{})
			delete(parameter, "examples")
			var illustration interface{} = "compiler-value"
			if schema, ok := parameter["schema"].(map[string]interface{}); ok && (schema["type"] == "integer" || schema["type"] == "number") {
				illustration = 1
			}
			parameter["example"] = illustration
		}
	}
	status := strconv.Itoa(pair.Response.Status)
	response := op["responses"].(map[string]interface{})[status].(map[string]interface{})
	op["responses"] = map[string]interface{}{status: response}
	content, _ := response["content"].(map[string]interface{})
	for _, v := range content {
		m := v.(map[string]interface{})
		e := m["examples"].(map[string]interface{})
		chosen, ok := e[name]
		if !ok {
			t.Fatal("missing published response")
		}
		m["examples"] = map[string]interface{}{name: chosen}
	}
	dir := t.TempDir()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "openapi.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	{
		hook := "import vertrag_hooks as hooks\n@hooks.before_each\ndef published_request(transaction):\n    transaction['fullPath'] = " + strconv.Quote(uri) + "\n"
		if name == "empty_body" {
			hook += "    transaction['request']['body'] = ''\n"
		} else if name == "request_too_large" {
			payload := "oversized"
			if len(oversizedBytes) != 0 {
				payload = strings.Repeat("x", oversizedBytes[0])
			}
			hook += "    transaction['request']['body'] = " + strconv.Quote(payload) + "\n"
		} else if name == "malformed_json" {
			hook += "    transaction['request']['body'] = '{'\n"
		} else if name == "body_boundary" {
			payload := string(pair.Request.Body)
			if len(payload) > 512 {
				t.Fatal("boundary example exceeds the configured cap")
			}
			hook += "    transaction['request']['body'] = " + strconv.Quote(payload+strings.Repeat(" ", 512-len(payload))) + "\n"
		}
		if err = os.WriteFile(filepath.Join(dir, "pair.py"), []byte(hook), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte("language: python\nhookfiles: ./pair.py\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(s.handler)
	defer server.Close()
	args := []string{"run", "--endpoint", server.URL, "--reporter", "json", "--output", "report.json"}
	if s.token != "" {
		args = append(args, "--header", "Authorization: Bearer "+s.token)
	}
	args = append(args, "openapi.json")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		if data, readErr := os.ReadFile(filepath.Join(dir, "report.json")); readErr == nil {
			var report map[string]json.RawMessage
			if json.Unmarshal(data, &report) == nil {
				for key, value := range report {
					var results []struct {
						Name   string
						Errors []string
						Beyond []string
					}
					if json.Unmarshal(value, &results) == nil {
						for _, result := range results {
							if len(result.Errors)+len(result.Beyond) > 0 {
								t.Logf("Vertrag %s %s: errors=%v beyond=%v", key, result.Name, result.Errors, result.Beyond)
							}
						}
					}
				}
			}
		}
		t.Fatalf("Vertrag rejected bulk pair: %v\n%s", err, output)
	}
	data, err = os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Summary struct{ Total, Passing, Failing, Errors, Skipped int }
	}
	if err = json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.Total != 1 || report.Summary.Passing != 1 || report.Summary.Failing != 0 || report.Summary.Errors != 0 || report.Summary.Skipped != 0 {
		t.Fatalf("exchange did not run and pass: %s", data)
	}
}
