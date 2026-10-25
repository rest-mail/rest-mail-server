package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
)

var bulkQueueExamples = map[string]struct{ method, path string }{"bulkRetryQueue": {http.MethodPost, "/api/v1/admin/queue/bulk-retry"}, "bulkBounceQueue": {http.MethodPost, "/api/v1/admin/queue/bulk-bounce"}, "bulkDeleteQueue": {http.MethodDelete, "/api/v1/admin/queue/bulk-delete"}}

type bulkQueueExampleMutation struct {
	query string
	args  []driver.NamedValue
}
type bulkQueueExampleStore struct {
	mu    sync.Mutex
	calls []bulkQueueExampleMutation
}

func (s *bulkQueueExampleStore) snapshot() []bulkQueueExampleMutation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bulkQueueExampleMutation(nil), s.calls...)
}

type bulkQueueExampleConnection struct {
	capabilityExampleConnection
	store *bulkQueueExampleStore
	fail  bool
}

func (c bulkQueueExampleConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c bulkQueueExampleConnection) Driver() driver.Driver                        { return c }
func (c bulkQueueExampleConnection) Open(string) (driver.Conn, error)             { return c, nil }

type bulkQueueExampleTx struct{}

func (bulkQueueExampleTx) Commit() error                       { return nil }
func (bulkQueueExampleTx) Rollback() error                     { return nil }
func (c bulkQueueExampleConnection) Begin() (driver.Tx, error) { return bulkQueueExampleTx{}, nil }

// The database/sql boundary captures the actual GORM update/delete, including
// selectors and transition guards. No live row is changed and no PostgreSQL
// selection, transaction, concurrency or audit-persistence proof is claimed.
func (c bulkQueueExampleConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.Contains(query, `"outbound_queue"`) {
		return nil, errors.New("unexpected write outside queue fixture")
	}
	c.store.mu.Lock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{query, append([]driver.NamedValue(nil), args...)})
	c.store.mu.Unlock()
	if c.fail {
		return nil, errors.New("private queue mutation detail")
	}
	affected := int64(3)
	for _, a := range args {
		switch a.Value {
		case int64(987654):
			affected = 1
		case int64(0), "no-match.test":
			affected = 0
		}
	}
	if strings.Contains(query, "id IN") && !strings.Contains(query, "987654") {
		for _, a := range args {
			if a.Value == int64(1) {
				affected = 2
			}
		}
	}
	return driver.RowsAffected(affected), nil
}

type bulkQueueExampleScenario struct {
	accountReadScenario
	store *bulkQueueExampleStore
}

func bulkQueueExampleScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previous := heldGrants
	defer func() { heldGrants = previous }()
	makeRouter := func(entries []grants.Entry, permissionsFail, writeFail, small bool) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if permissionsFail {
					return grants.Held{}, errors.New("private queue permission detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		pool := sql.OpenDB(bulkQueueExampleConnection{store: store, fail: writeFail})
		t.Cleanup(func() { _ = pool.Close() })
		limit := int64(65536)
		if small {
			limit = 8
		}
		r := NewRouters(openAccountReadDB(t, pool), jwt, &config.Config{Environment: "test", APIMaxBodyBytes: limit}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{r.Public, token}, store}
	}
	all := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(all, false, false, false)
	out := map[string]bulkQueueExampleScenario{}
	for _, name := range []string{"by_ids", "invalid_body", "invalid_ids", "selection_missing", "empty_ids", "null_filter", "by_filter", "ids_take_precedence", "empty_filter", "no_match", "negative_id", "too_many_ids", "unknown_field", "null_body", "invalid_sender", "invalid_recipient"} {
		out[name] = normal
	}
	out["internal_error"] = makeRouter(all, false, true, false)
	out["request_too_large"] = makeRouter(all, false, false, true)
	out["required_capability"] = makeRouter([]grants.Entry{{Capability: middleware.CapQueueManage, Scope: grants.All}}, false, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapQueueManage, Scope: grants.Domain("example.com")}}, false, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapQueueRead, Scope: grants.All}}, false, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false, false)
	out["unauthenticated"] = bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}
	out["session_unavailable"] = bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}
	setup, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	out["setup_required"] = bulkQueueExampleScenario{accountReadScenario{normal.handler, setup.AccessToken}, normal.store}
	return out
}

func TestBulkQueueCompleteExamples(t *testing.T) {
	scenarios := bulkQueueExampleScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range bulkQueueExamples {
		if len(pairs[operation]) < 15 {
			t.Fatalf("incomplete %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				var before int
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				started := time.Now()
				req := httptest.NewRequest(route.method, route.path, bytes.NewReader(pair.Request.Body))
				req.Header.Set("Content-Type", "application/json")
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != "application/json" {
					t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				var mutations []bulkQueueExampleMutation
				if s.store != nil {
					mutations = s.store.snapshot()[before:]
				}
				wantMutation := rr.Code == 200 || name == "internal_error"
				if (len(mutations) == 1) != wantMutation || len(mutations) > 1 {
					t.Fatalf("mutation attempts=%d for status %d", len(mutations), rr.Code)
				}
				if !wantMutation {
					return
				}
				m := mutations[0]
				values := make([]interface{}, len(m.args))
				for i, a := range m.args {
					values[i] = a.Value
				}
				has := func(want interface{}) bool {
					for _, v := range values {
						if v == want {
							return true
						}
					}
					return false
				}
				if operation == "bulkDeleteQueue" {
					if !strings.HasPrefix(m.query, `DELETE FROM "outbound_queue"`) {
						t.Fatalf("wrong deletion: %s", m.query)
					}
				} else {
					if !strings.HasPrefix(m.query, `UPDATE "outbound_queue" SET`) || !strings.Contains(m.query, "status IN") {
						t.Fatalf("mutation lost transition guard: %s", m.query)
					}
					for _, status := range []string{"pending", "deferred"} {
						if !has(status) {
							t.Fatalf("missing transition status %s", status)
						}
					}
					if operation == "bulkRetryQueue" {
						index := regexp.MustCompile(`"next_attempt"\s*=\s*\$(\d+)`).FindStringSubmatch(m.query)
						if has("delivering") || len(index) != 2 {
							t.Fatalf("retry changed allowed states or lost next attempt: %s", m.query)
						}
						ordinal, err := strconv.Atoi(index[1])
						if err != nil || ordinal < 1 || ordinal > len(values) {
							t.Fatal("invalid next-attempt placeholder")
						}
						stamp, ok := values[ordinal-1].(time.Time)
						if !ok || stamp.Before(started) || stamp.After(time.Now()) {
							t.Fatal("retry timestamp is not the request time")
						}
					} else if !has("delivering") || !has("bounced") || !has("manually bounced by admin (bulk)") {
						t.Fatal("bounce lost allowed states or reason")
					}
				}
				if name == "by_ids" || name == "required_capability" || name == "internal_error" {
					if !strings.Contains(m.query, "id IN") || !has(int64(987654)) {
						t.Fatalf("lost ID selector: %s %v", m.query, values)
					}
				}
				if name == "ids_take_precedence" {
					if !strings.Contains(m.query, "id IN") || strings.Contains(m.query, "ILIKE") || has("no-match.test") || !has(int64(1)) || !has(int64(2)) {
						t.Fatalf("filter overrode IDs: %s %v", m.query, values)
					}
				}
				if name == "by_filter" {
					for _, part := range []string{"status =", "domain =", "sender ILIKE", "recipient ILIKE", "ESCAPE"} {
						if !strings.Contains(m.query, part) {
							t.Fatalf("lost filter %s: %s", part, m.query)
						}
					}
					if !has("%sender\\%\\_\\\\%") || !has("%recipient\\%\\_\\\\%") {
						t.Fatalf("lost literal filters: %v", values)
					}
				}
				if name == "empty_filter" && operation == "bulkDeleteQueue" && strings.Contains(m.query, "WHERE") {
					t.Fatalf("empty filter stopped selecting whole fixture: %s", m.query)
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := bulkQueueExamples[w.OperationID]; ok {
				t.Errorf("bulk queue warning: %v", w)
			}
		}
	}
}

// Isolate the published pair while preserving the registered request and
// response schemas. A standard hook sends the canonical URI (including invalid
// path bindings) and the transport-cap violation; expected answers stay intact.
func checkQueueMutationPairWithVertrag(t *testing.T, binary string, s bulkQueueExampleScenario, method, path, name string, pair oapifly.ExamplePair, oversizedBytes ...int) {
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
func TestBulkQueueCompleteExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for bulk queue exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := bulkQueueExampleScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range bulkQueueExamples {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				checkQueueMutationPairWithVertrag(t, binary, scenarios[name], route.method, route.path, name, pair)
			})
		}
	}
}
