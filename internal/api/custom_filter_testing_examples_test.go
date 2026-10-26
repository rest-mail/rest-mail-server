package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
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
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/internal/pipeline"
)

const customFilterTestingPath = "/api/v1/admin/custom-filters/{id}/test"

type customFilterTestingContextKey struct{}
type customFilterTestingConnection struct{ domainMutationConnection }

func (c *customFilterTestingConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *customFilterTestingConnection) Driver() driver.Driver                        { return c }
func (c *customFilterTestingConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *customFilterTestingConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, `FROM "custom_filters"`) {
		c.record(q, args)
		c.record("EXECUTE_CONTEXT", []driver.NamedValue{{Value: ctx.Value(customFilterTestingContextKey{}) == true}})
		if len(args) != 2 || !strings.Contains(q, `"custom_filters"."id" =`) || args[1].Value != int64(1) {
			return nil, errors.New("execution lookup lost exact ID or row bound")
		}
		id := args[0].Value.(int64)
		if id == 3 {
			return nil, errors.New("private custom-filter lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "config"}}
		if id == 2147483647 {
			return r, nil
		}
		script := map[int64]string{1: "normal", 5: "syntax-error", 6: "execution-timeout", 7: "too-large", 8: "worker-error", 9: "token-refused", 10: "invalid-json", 11: "truncated-response", 12: "transport-unavailable", 13: "spaced-response"}[id]
		config, _ := json.Marshal(map[string]string{"script": script})
		if id == 2 {
			config = []byte(`{}`)
		}
		if id == 4 {
			config = []byte(`broken JSON`)
		}
		r.rows = [][]driver.Value{{id, config}}
		return r, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}
func (c *customFilterTestingConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(q, `"custom_filters"`) || strings.Contains(q, `"messages"`) || strings.Contains(q, `"message_traces"`) {
		c.record("EXECUTE_WRITE", args)
	}
	return nil, errors.New("execution preview must not persist changes")
}
func newCustomFilterTestingRouters(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":        {Capability: middleware.CapPipelinesWrite, Scope: "*"},
		"key_read":         {Capability: middleware.CapPipelinesRead, Scope: "*"},
		"key_domain":       {Capability: middleware.CapPipelinesWrite, Scope: "domain:example.com"},
		"key_mailbox":      {Capability: middleware.CapPipelinesWrite, Scope: "mailbox:owner@example.com"},
		"key_wrong_domain": {Capability: middleware.CapPipelinesWrite, Scope: "domain:other.test"},
	} {
		value, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		scope, err := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{p}})
		if err != nil {
			t.Fatal(err)
		}
		keys[public] = accountListKey{accountReadKey{hash: hash, scope: scope}, models.CredentialKindAPIKey}
		tokens[name] = value
	}
	previous := heldGrants
	defer func() { heldGrants = previous }()
	makeRouter := func(entries []grants.Entry, fail, limited bool) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if fail {
					return grants.Held{}, errors.New("private domain grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &customFilterTestingConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }

		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, PipelineTestRateLimitEnabled: limited, PipelineTestRateLimitRPS: 1.0 / 3600, PipelineTestRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req = req.WithContext(context.WithValue(req.Context(), customFilterTestingContextKey{}, true))
			r.Public.ServeHTTP(w, req)
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"testCustomFilter"} {
		for name := range pairs[op] {
			out[name] = normal
		}
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapPipelinesWrite, Scope: grants.All}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapPipelinesRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapPipelinesWrite, Scope: grants.Domain("example.com")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapPipelinesWrite, Scope: grants.Mailbox("owner@example.com")}}, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false)
	out["unauthenticated"] = bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}
	out["session_unavailable"] = bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}
	for name, key := range tokens {
		s := normal
		s.token = key
		out[name] = s
	}
	missing := out["read_only"]
	missing.token = tokens["key_write"]
	out["key_holder_missing"] = missing
	limited := makeRouter(wild, false, true)
	out["rate_limited"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		warmup := r.Clone(r.Context())
		warmup.Body = io.NopCloser(strings.NewReader("{"))
		warmup.ContentLength = 1
		limited.handler.ServeHTTP(httptest.NewRecorder(), warmup)
		limited.handler.ServeHTTP(w, r)
	}), token}, limited.store}
	for _, name := range []string{"cookie_session", "csrf_missing", "csrf_mismatch"} {
		name := name
		out[name] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: token})
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "domain-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "domain-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong")
			}
			normal.handler.ServeHTTP(w, r)
		}), ""}, normal.store}
	}
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: middleware.CapPipelinesWrite, Scope: grants.Domain("other.test")}}, false, false)

	return out
}

func newCustomFilterTestingScenarios(t *testing.T) (map[string]bulkQueueExampleScenario, *customFilterValidationCalls) {
	t.Helper()
	calls := &customFilterValidationCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/execute" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer "+customFilterValidationToken {
			t.Error("incorrect execution sidecar wiring")
			w.WriteHeader(401)
			return
		}
		var request map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		calls.mu.Lock()
		calls.requests = append(calls.requests, request)
		calls.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch request["script"] {
		case "syntax-error":
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"Unexpected token"}`)
		case "execution-timeout":
			w.WriteHeader(408)
			io.WriteString(w, `{"error":"Execution timed out"}`)
		case "too-large":
			w.WriteHeader(413)
			io.WriteString(w, `{"error":"Email exceeds sidecar limit"}`)
		case "worker-error":
			w.WriteHeader(500)
			io.WriteString(w, `{"error":"Worker unavailable"}`)
		case "token-refused":
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"unauthorized"}`)
		case "invalid-json":
			io.WriteString(w, "{")
		case "truncated-response":
			b := `{"result":{},"logs":[]}`
			w.Header().Set("Content-Length", strconv.Itoa(len(b)+11))
			io.WriteString(w, b)
		case "transport-unavailable":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		case "spaced-response":
			io.WriteString(w, " {\"result\":{\"custom\":42}, \"logs\":[\"checked\"]}\n")
		default:
			io.WriteString(w, `{"result":{"action":"continue"},"logs":[]}`)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("JS_FILTER_URL", server.URL)
	t.Setenv("JS_FILTER_TOKEN", customFilterValidationToken)
	return newCustomFilterTestingRouters(t), calls
}
func TestCustomFilterTestingFailures(t *testing.T) {
	f, _ := newCustomFilterTestingScenarios(t)
	for _, id := range []int{3, 10, 11} {
		t.Run(strconv.Itoa(id), func(t *testing.T) {
			rr := mailboxSessionEndingRequest(t, f["valid"], http.MethodPost, "/api/v1/admin/custom-filters/"+strconv.Itoa(id)+"/test", []byte(`{}`))
			if rr.Code != 500 || !json.Valid(rr.Body.Bytes()) || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("failure got%d %s want sanitized500", rr.Code, rr.Body)
			}
		})
	}
}
func TestCustomFilterTestingForwardedBytes(t *testing.T) {
	f, _ := newCustomFilterTestingScenarios(t)
	rr := mailboxSessionEndingRequest(t, f["valid"], http.MethodPost, "/api/v1/admin/custom-filters/13/test", []byte(`{}`))
	want := " {\"result\":{\"custom\":42}, \"logs\":[\"checked\"]}\n"
	if rr.Code != 200 || rr.Body.String() != want {
		t.Fatalf("forwarded%d %q want200 %q", rr.Code, rr.Body.String(), want)
	}
}
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
				json.Unmarshal(pair.Request.Body, &request)
				email := request["email"]
				if email == nil {
					raw, _ := json.Marshal(pipeline.EmailJSON{Envelope: pipeline.Envelope{MailFrom: "test@example.com", RcptTo: []string{"user@example.com"}, Direction: "inbound"}, Headers: pipeline.Headers{From: []pipeline.Address{{Address: "test@example.com"}}, To: []pipeline.Address{{Address: "user@example.com"}}, Subject: "Test email"}, Body: pipeline.Body{ContentType: "text/plain", Content: "This is a test email for filter testing."}})
					json.Unmarshal(raw, &email)
				}
				id := int64(1)
				if v, ok := pair.Request.Path["id"]; ok {
					b, _ := json.Marshal(v)
					json.Unmarshal(b, &id)
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
