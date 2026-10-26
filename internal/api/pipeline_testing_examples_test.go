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
	"github.com/restmail/restmail/internal/pipeline/filters"
)

var pipelineTestingPaths = map[string]string{"testFilter": "/api/v1/admin/pipelines/test-filter", "testPipeline": "/api/v1/admin/pipelines/test"}

type pipelineTestingContextKey struct{}
type pipelineTestingFault struct {
	mode   string
	cancel context.CancelFunc
}

func (pipelineTestingFault) Name() string              { return "header_validate" }
func (pipelineTestingFault) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (f pipelineTestingFault) Execute(ctx context.Context, _ *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	if f.mode == "execution_panic" {
		panic("private filter runtime panic")
	}
	if f.mode == "execution_cancelled" {
		f.cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, errors.New("private filter runtime failure")
}

type pipelineTestingConnection struct {
	domainMutationConnection
	cancel context.CancelFunc
}

func (c *pipelineTestingConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *pipelineTestingConnection) Driver() driver.Driver                        { return c }
func (c *pipelineTestingConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *pipelineTestingConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, `FROM "pipelines"`) {
		c.record(q, args)
		c.record("PREVIEW_CONTEXT", []driver.NamedValue{{Value: ctx.Value(pipelineTestingContextKey{}) == true}})
		if len(args) != 2 || !strings.Contains(q, `"pipelines"."id" =`) || args[1].Value != int64(1) {
			return nil, errors.New("pipeline preview lost exact ID or row bound")
		}
		id := args[0].Value.(int64)
		if id == 3 {
			return nil, errors.New("private pipeline preview lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "direction", "filters", "active", "created_at", "updated_at"}}
		if id == 2147483647 {
			return r, nil
		}
		config := []byte(`[{"name":"header_validate","type":"action","enabled":true,"config":{}}]`)
		if id == 2 {
			config = []byte(`[]`)
		}
		if id == 4 {
			config = []byte(`broken JSON`)
		}
		r.rows = [][]driver.Value{{id, int64(1), "inbound", config, true, pipelineMutationTime, pipelineMutationTime}}
		return r, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}
func (c *pipelineTestingConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(q, `"pipelines"`) || strings.Contains(q, `"messages"`) || strings.Contains(q, `"message_traces"`) {
		c.record("PREVIEW_WRITE", args)
	}
	return nil, errors.New("pipeline preview must not persist changes")
}
func newPipelineTestingScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
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
		c := &pipelineTestingConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }

		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8192, PipelineTestRateLimitEnabled: limited, PipelineTestRateLimitRPS: 1.0 / 3600, PipelineTestRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.reads = 0
			c.current = nil
			c.createdName = ""
			if req.Header.Get("X-Example-Cancel") == "after-pipeline-lookup" {
				var ctx context.Context
				ctx, c.cancel = context.WithCancel(req.Context())
				defer func() { c.cancel(); c.cancel = nil }()
				req = req.WithContext(ctx)
			}
			req = req.WithContext(context.WithValue(req.Context(), pipelineTestingContextKey{}, true))
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
	for _, op := range []string{"testFilter", "testPipeline"} {
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

	out["cancelled_after_lookup"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Example-Cancel", "after-pipeline-lookup")
		normal.handler.ServeHTTP(w, r)
	}), token}, normal.store}

	for _, mode := range []string{"execution_failure", "execution_panic", "execution_cancelled"} {
		mode := mode
		out[mode] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			pipeline.DefaultRegistry.Register("header_validate", func([]byte) (pipeline.Filter, error) { return pipelineTestingFault{mode: mode, cancel: cancel}, nil })
			defer pipeline.DefaultRegistry.Register("header_validate", filters.NewHeaderValidate)
			normal.handler.ServeHTTP(w, r)
		}), token}, normal.store}
	}
	return out
}

func TestPipelineTestingLookupAndExecutionFailures(t *testing.T) {
	f := newPipelineTestingScenarios(t)
	rr := mailboxSessionEndingRequest(t, f["write_only"], http.MethodPost, pipelineTestingPaths["testPipeline"], []byte(`{"pipeline_id":3,"email":{}}`))
	if rr.Code != 500 {
		t.Errorf("lookup got%d want500", rr.Code)
	}
	for _, name := range []string{"execution_failure", "execution_panic", "execution_cancelled"} {
		t.Run(name, func(t *testing.T) {
			rr := mailboxSessionEndingRequest(t, f[name], http.MethodPost, pipelineTestingPaths["testFilter"], []byte(`{"filter_name":"header_validate","config":{},"email":{}}`))
			if rr.Code != 500 {
				t.Fatalf("runtime got%d want500", rr.Code)
			}
			if !json.Valid(rr.Body.Bytes()) || strings.Contains(rr.Body.String(), "private filter") {
				t.Fatal("runtime error escaped the typed sanitized boundary")
			}
		})
	}
}
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
