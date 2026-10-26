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
	rmcrypto "github.com/restmail/restmail/internal/crypto"
)

func certificateCreationRequestBody(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	body = certificateCreationBody(t, body)
	if name == "malformed_json" {
		return []byte("{")
	}
	if name == "request_too_large" {
		return bytes.Repeat([]byte("x"), certificateCreationBodyLimit+1)
	}
	if name == "body_boundary" {
		body = append(body, bytes.Repeat([]byte(" "), certificateCreationBodyLimit-len(body))...)
	}
	return body
}

func TestCertificateCreationExamples(t *testing.T) {
	f := newCertificateCreationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["createCertificate"]) != 40 {
		t.Fatal("certificate pair count changed")
	}
	for name, pair := range pairs["createCertificate"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatal("missing certificate scenario")
			}
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			rr := mailboxSessionEndingRequest(t, s, http.MethodPost, certificateCreationPath, certificateCreationRequestBody(t, name, pair.Request.Body))
			if bytes.Contains(rr.Body.Bytes(), []byte(signingSetterKey(t))) || bytes.Contains(rr.Body.Bytes(), []byte(certificateCreationMaster)) {
				t.Fatal("private material exposed")
			}
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d want%d", rr.Code, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			if s.store == nil {
				return
			}
			calls := s.store.snapshot()[before:]
			inserts := 0
			for _, q := range calls {
				if strings.HasPrefix(q.query, `INSERT INTO "certificates"`) {
					inserts++
					v, err := exampleInsertFields(q.query, q.args)
					if err != nil {
						t.Fatal(err)
					}
					plain, err := rmcrypto.DecryptString(v["key_pem"].(string), certificateCreationMaster)
					if err != nil || plain != signingSetterKey(t) {
						t.Fatal("certificate key did not round-trip encrypted")
					}
					var req struct {
						DomainID  int64 `json:"domain_id"`
						AutoRenew *bool `json:"auto_renew"`
					}
					if err := json.Unmarshal(pair.Request.Body, &req); err != nil {
						t.Fatal(err)
					}
					want := req.AutoRenew == nil || *req.AutoRenew
					if v["domain_id"] != req.DomainID || v["auto_renew"] != want {
						t.Fatal("certificate domain or renewal policy changed")
					}
				}
			}
			begins, commits, rollbacks := 0, 0, 0
			for _, q := range calls {
				switch q.query {
				case "BEGIN":
					begins++
				case "COMMIT":
					commits++
				case "ROLLBACK":
					rollbacks++
				}
			}
			if inserts > 0 {
				if begins != 1 || commits+rollbacks != 1 || (commits == 1) != (rr.Code == 201) {
					t.Fatal("certificate upload did not commit or roll back exactly once")
				}
			} else if begins+commits+rollbacks != 0 {
				t.Fatal("early refusal began an upload transaction")
			}
			want := rr.Code == 201 || name == "insert_unavailable" || name == "reload_unavailable" || name == "reload_missing" || name == "association_unavailable"
			if (inserts == 1) != want || inserts > 1 {
				t.Fatal("unexpected certificate mutation count")
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "createCertificate" {
				t.Errorf("certificate warning:%v", w)
			}
		}
	}
}

func TestCertificateCreationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newCertificateCreationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["createCertificate"] {
		t.Run(name, func(t *testing.T) {
			checkCertificateCreationPairWithVertrag(t, binary, f[name], http.MethodPost, certificateCreationPath, name, pair, certificateCreationBodyLimit+1)
		})
	}
}

func checkCertificateCreationPairWithVertrag(t *testing.T, binary string, s bulkQueueExampleScenario, method, path, name string, pair oapifly.ExamplePair, oversizedBytes ...int) {
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
		cert, key := certificateCreationMaterial(t)
		if err := os.WriteFile(filepath.Join(dir, "certificate.pem"), []byte(cert), 0600); err != nil {
			t.Fatal(err)
		}
		keyPath, err := filepath.Abs("../../tests/contract/fixtures/dkim-key.pem")
		if err != nil {
			t.Fatal(err)
		}
		hook += "    import json\n    from pathlib import Path\n    try:\n        body = json.loads(transaction['request']['body'])\n    except (ValueError, TypeError):\n        body = None\n    if isinstance(body, dict):\n        if body.get('cert_pem') == 'certificate':\n            body['cert_pem'] = Path('certificate.pem').read_text()\n        if body.get('key_pem') == 'key':\n            body['key_pem'] = Path(" + strconv.Quote(keyPath) + ").read_text()\n        transaction['request']['body'] = json.dumps(body)\n"
		_ = key

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
		t.Fatalf("Vertrag rejected certificate exchange: %v (request material withheld)", err)
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
