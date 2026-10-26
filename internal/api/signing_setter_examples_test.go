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
	"github.com/restmail/restmail/internal/db/models"
)

var signingSetterPaths = map[string]string{"setDKIMKey": "/api/v1/admin/dkim/{id}", "setDKIMRotation": "/api/v1/admin/dkim/{id}/rotation"}

func signingSetterRequestBody(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	body = signingSetterBody(t, body)
	if name == "malformed_json" {
		return []byte("{")
	}
	if name == "request_too_large" {
		return bytes.Repeat([]byte("x"), signingSetterBodyLimit+1)
	}
	if name == "body_boundary" {
		if len(body) > signingSetterBodyLimit {
			t.Fatal("signing fixture exceeds body boundary")
		}
		body = append(body, bytes.Repeat([]byte(" "), signingSetterBodyLimit-len(body))...)
	}
	return body
}

func TestSigningSetterExamples(t *testing.T) {
	f := newSigningSetterScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"setDKIMKey": 44, "setDKIMRotation": 42}
	for op, path := range signingSetterPaths {
		if len(pairs[op]) != want[op] {
			t.Fatalf("%s pairs:%d want%d", op, len(pairs[op]), want[op])
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f[name]
				if !ok {
					t.Fatalf("missing signing scenario:%s", name)
				}
				body := signingSetterRequestBody(t, name, []byte(pair.Request.Body))
				before := 0
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				rr := mailboxSessionEndingRequest(t, s, http.MethodPut, messageCollectionReadURI(path, pair), body)
				if bytes.Contains(rr.Body.Bytes(), []byte(signingSetterKey(t))) || bytes.Contains(rr.Body.Bytes(), []byte(signingSetterMasterKey)) || bytes.Contains(rr.Body.Bytes(), []byte(models.DKIMKeyCipherPrefix)) {
					t.Fatal("signing material exposed")
				}
				if rr.Code != pair.Response.Status {
					t.Fatalf("got%d want%d", rr.Code, pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				var calls []bulkQueueExampleMutation
				if s.store != nil {
					calls = s.store.snapshot()[before:]
				}
				var writes []bulkQueueExampleMutation
				for _, q := range calls {
					if strings.HasPrefix(q.query, `UPDATE "domains"`) {
						writes = append(writes, q)
					}
				}
				wantWrite := rr.Code == 200 || name == "update_unavailable" || name == "concurrent" || name == "status_unavailable"
				if (len(writes) == 1) != wantWrite || len(writes) > 1 {
					t.Fatalf("signing writes%d want%t", len(writes), wantWrite)
				}
				if len(writes) > 0 {
					fields, err := signingSetterFields(writes[0].query, writes[0].args)
					if err != nil {
						t.Fatal(err)
					}
					if fields["id"] != int64(pair.Request.Path["id"].(float64)) {
						t.Fatal("signing changed another domain")
					}
					if op == "setDKIMKey" {
						plain, err := models.LoadDKIMPrivateKey(fields["dkim_private_key"].(string), signingSetterMasterKey)
						if err != nil || plain != signingSetterKey(t) {
							t.Fatal("encrypted signing value did not round-trip")
						}
						var answer struct {
							Data struct {
								CreatedAt *time.Time `json:"created_at"`
							}
						}
						if rr.Code == 200 {
							if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
								t.Fatal(err)
							}
							if answer.Data.CreatedAt == nil {
								if fields["dkim_created_at"] != nil {
									t.Fatal("legacy key acquired an invented timestamp")
								}
							} else {
								at, ok := fields["dkim_created_at"].(time.Time)
								if !ok || !at.Equal(*answer.Data.CreatedAt) {
									t.Fatal("saved key timestamp differs from answer")
								}
							}
						}
					} else {
						var request struct {
							Days int `json:"rotation_interval_days"`
						}
						if err := json.Unmarshal(pair.Request.Body, &request); err != nil {
							t.Fatal(err)
						}
						if fields["dkim_rotation_interval_days"] != int64(request.Days) {
							t.Fatal("rotation interval changed in persistence")
						}
					}
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := signingSetterPaths[w.OperationID]; ok {
				t.Errorf("signing setter warning:%v", w)
			}
		}
	}
}

func TestSigningSetterExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for signing exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newSigningSetterScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range signingSetterPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f[name]
				if !ok {
					t.Fatalf("missing signing scenario:%s", name)
				}
				checkSigningSetterPairWithVertrag(t, binary, s, http.MethodPut, path, name, pair, signingSetterBodyLimit+1)
			})
		}
	}
}

func checkSigningSetterPairWithVertrag(t *testing.T, binary string, s bulkQueueExampleScenario, method, path, name string, pair oapifly.ExamplePair, oversizedBytes ...int) {
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
			if len(payload) > 512 {
				t.Fatal("boundary example exceeds the configured cap")
			}
			hook += "    transaction['request']['body'] = " + strconv.Quote(payload+strings.Repeat(" ", 512-len(payload))) + "\n"
		}
		if path == signingSetterPaths["setDKIMKey"] {
			keyPath, err := filepath.Abs("../../tests/contract/fixtures/dkim-key.pem")
			if err != nil {
				t.Fatal(err)
			}
			hook += "    import json\n    from pathlib import Path\n    try:\n        body = json.loads(transaction['request']['body'])\n    except (ValueError, TypeError):\n        body = None\n    if isinstance(body, dict) and body.get('private_key') == 'fixture-signing-key':\n        body['private_key'] = Path(" + strconv.Quote(keyPath) + ").read_text()\n        transaction['request']['body'] = json.dumps(body)\n"
		}
		if name == "body_boundary" {
			hook += "    payload = transaction['request']['body']\n    transaction['request']['body'] = payload + ' ' * (8192 - len(payload.encode('utf-8')))\n"
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
	_, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Vertrag rejected signing exchange: %v (request material withheld)", err)
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
