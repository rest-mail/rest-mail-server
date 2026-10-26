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
	"github.com/restmail/restmail/internal/pipeline/filters"
)

func assertPipelineTestingBody(t *testing.T, wantRaw json.RawMessage, gotRaw []byte) {
	t.Helper()
	var want, got interface{}
	if json.Unmarshal(wantRaw, &want) != nil || json.Unmarshal(gotRaw, &got) != nil {
		t.Fatal("preview answer is not JSON")
	}
	var timing func(interface{}, interface{})
	timing = func(expected, actual interface{}) {
		switch e := expected.(type) {
		case map[string]interface{}:
			a, ok := actual.(map[string]interface{})
			if !ok {
				return
			}
			for k, v := range e {
				if k == "duration_ms" {
					n, ok := a[k].(float64)
					if !ok || n < 0 {
						t.Fatal("preview timing missing or invalid")
					}
					e[k] = n
				} else {
					timing(v, a[k])
				}
			}
		case []interface{}:
			a, ok := actual.([]interface{})
			if !ok || len(a) != len(e) {
				return
			}
			for i, v := range e {
				timing(v, a[i])
			}
		}
	}
	timing(want, got)
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicExampleBody(t, encoded, gotRaw)
}

func TestPipelineTestingExamples(t *testing.T) {
	f := newPipelineTestingScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range pipelineTestingPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f[name]
				if !ok {
					t.Fatal("missing preview scenario")
				}
				body := []byte(pair.Request.Body)
				if name == "malformed_json" {
					body = []byte("{")
				}
				if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 8193)
				}
				if name == "body_boundary" {
					body = append(body, bytes.Repeat([]byte(" "), 8192-len(body))...)
				}
				before := 0
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				rr := mailboxSessionEndingRequest(t, s, http.MethodPost, path, body)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got%d want%d", rr.Code, pair.Response.Status)
				}
				assertPipelineTestingBody(t, pair.Response.Body, rr.Body.Bytes())
				if s.store != nil {
					for _, call := range s.store.snapshot()[before:] {
						if call.query == "PREVIEW_WRITE" {
							t.Fatal("preview attempted a pipeline or message write")
						}
						if call.query == "PREVIEW_CONTEXT" && (len(call.args) != 1 || call.args[0].Value != true) {
							t.Fatal("pipeline lookup lost HTTP context")
						}
					}
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := pipelineTestingPaths[w.OperationID]; ok {
				t.Errorf("preview warning:%v", w)
			}
		}
	}
}

func TestPipelineTestingExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newPipelineTestingScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range pipelineTestingPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				checkPipelineTestingPairWithVertrag(t, binary, f[name], http.MethodPost, path, name, pair, 8193)
			})
		}
	}
}

func checkPipelineTestingPairWithVertrag(t *testing.T, binary string, s bulkQueueExampleScenario, method, path, name string, pair oapifly.ExamplePair, oversizedBytes ...int) {
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
		if name == "request_too_large" {
			payload := "oversized"
			if len(oversizedBytes) != 0 {
				payload = strings.Repeat("x", oversizedBytes[0])
			}
			hook += "    transaction['request']['body'] = " + strconv.Quote(payload) + "\n"
		} else if name == "malformed_json" {
			hook += "    transaction['request']['body'] = '{'\n"
		} else if name == "body_boundary" {
			payload := string(pair.Request.Body)
			if len(payload) > 8192 {
				t.Fatal("boundary example exceeds the configured cap")
			}
			hook += "    transaction['request']['body'] = " + strconv.Quote(payload+strings.Repeat(" ", 8192-len(payload))) + "\n"
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

type pipelineTestingObservedFilter struct {
	pipeline.Filter
	seen chan bool
}

func (f pipelineTestingObservedFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	f.seen <- ctx.Value(pipelineTestingContextKey{}) == true
	return f.Filter.Execute(ctx, email)
}

func TestPipelineTestingExecutionReceivesHTTPContext(t *testing.T) {
	f := newPipelineTestingScenarios(t)
	seen := make(chan bool, 2)
	pipeline.DefaultRegistry.Register("header_validate", func(config []byte) (pipeline.Filter, error) {
		filter, err := filters.NewHeaderValidate(config)
		return pipelineTestingObservedFilter{Filter: filter, seen: seen}, err
	})
	defer pipeline.DefaultRegistry.Register("header_validate", filters.NewHeaderValidate)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range pipelineTestingPaths {
		rr := mailboxSessionEndingRequest(t, f["write_only"], http.MethodPost, path, pairs[op]["valid"].Request.Body)
		if rr.Code != 200 {
			t.Fatalf("%s status%d", op, rr.Code)
		}
		select {
		case ok := <-seen:
			if !ok {
				t.Fatal("preview lost HTTP context before filter execution")
			}
		case <-time.After(time.Second):
			t.Fatal("preview did not execute its filter")
		}
	}
}
