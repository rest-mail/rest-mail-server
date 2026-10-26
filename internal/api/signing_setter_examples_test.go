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
	"gorm.io/gorm"
)

var signingSetterPaths = map[string]string{"setDKIMKey": "/api/v1/admin/dkim/{id}", "setDKIMRotation": "/api/v1/admin/dkim/{id}/rotation"}

const signingSetterMasterKey = "public-test-signing-fixture-master-key"
const signingSetterBodyLimit = 8192

var signingSetterTime = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func signingSetterKey(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../tests/contract/fixtures/dkim-key.pem")
	if err != nil {
		t.Fatal("read public contract signing fixture")
	}
	return string(b)
}

// Match the established contract hook: replace only the documented placeholder.
// Malformed bodies, missing fields, wrong types and invalid key material stay intact.
func signingSetterBody(t *testing.T, body []byte) []byte {
	t.Helper()
	var v map[string]interface{}
	if json.Unmarshal(body, &v) == nil && v["private_key"] == "fixture-signing-key" {
		v["private_key"] = signingSetterKey(t)
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return body
}

type signingSetterConnection struct {
	domainMutationConnection
	stored string
	target int64
	cancel context.CancelFunc
}

func signingSetterFields(q string, args []driver.NamedValue) (map[string]driver.Value, error) {
	return vacationMutationFields(strings.Replace(q, " WHERE id = ", ` WHERE "id" = `, 1), args)
}
func (c *signingSetterConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *signingSetterConnection) Driver() driver.Driver                        { return c }
func (c *signingSetterConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *signingSetterConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, `FROM "domains"`) {
		c.record(q, args)
		if strings.HasPrefix(q, `SELECT "name"`) {
			if len(args) != 1 || !strings.Contains(q, "id =") {
				return nil, errors.New("signing scope lost ID")
			}
			if args[0].Value == int64(8) {
				return nil, errors.New("private signing scope detail")
			}
			r := &authReadSnapshotRows{columns: []string{"name"}}
			if args[0].Value != int64(2147483647) {
				r.rows = [][]driver.Value{{"example.com"}}
			}
			return r, nil
		}
		if len(args) != 2 || !strings.Contains(q, `"domains"."id" =`) || args[1].Value != int64(1) {
			return nil, errors.New("signing lookup lost exact ID/bound")
		}
		c.target = args[0].Value.(int64)
		if c.target == 3 {
			return nil, errors.New("private signing lookup detail")
		}
		columns := []string{"id", "name", "dkim_selector", "dkim_private_key", "dkim_created_at", "dkim_rotation_interval_days"}
		r := &authReadSnapshotRows{columns: columns}
		if c.target == 2147483647 {
			return r, nil
		}
		var at driver.Value = signingSetterTime
		key := c.stored
		if c.target == 2 {
			key = "not-a-current-key"
		}
		if c.target == 10 {
			at = nil
		}
		r.rows = [][]driver.Value{{c.target, "example.com", "contract", key, at, int64(180)}}
		return r, nil
	}
	if strings.Contains(q, `FROM "dkim_rotation_keys"`) {
		c.record(q, args)
		if len(args) != 1 || args[0].Value != c.target || !strings.Contains(q, "domain_id =") {
			return nil, errors.New("rotation status lost domain")
		}
		if c.target == 7 {
			return nil, errors.New("private rotation status detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}}, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}
func (c *signingSetterConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "domains" SET`) {
		return nil, errors.New("unexpected ancillary signing write")
	}
	c.mutation(q, args)
	f, err := signingSetterFields(q, args)
	if err != nil {
		return nil, err
	}
	if f["id"] != c.target {
		return nil, errors.New("signing update lost domain")
	}
	if _, rotation := f["dkim_rotation_interval_days"]; rotation {
		if len(f) != 3 {
			return nil, errors.New("rotation update changed signing fields")
		}
	} else {
		if len(f) != 5 || f["dkim_selector"] == nil || f["dkim_private_key"] == nil {
			return nil, errors.New("key update changed unrelated fields")
		}
		stored, ok := f["dkim_private_key"].(string)
		if !ok {
			return nil, errors.New("missing encrypted signing value")
		}
		plain, err := models.LoadDKIMPrivateKey(stored, signingSetterMasterKey)
		if err != nil || !strings.HasPrefix(stored, models.DKIMKeyCipherPrefix) || plain == "" {
			return nil, errors.New("signing material not encrypted at rest")
		}
	}
	if c.target == 4 {
		return nil, errors.New("private signing update detail")
	}
	if c.target == 6 {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}
func newSigningSetterScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	fixture := signingSetterKey(t)
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":        {Capability: middleware.CapDomainsWrite, Scope: "*"},
		"key_read":         {Capability: middleware.CapDomainsRead, Scope: "*"},
		"key_domain":       {Capability: middleware.CapDomainsWrite, Scope: "domain:example.com"},
		"key_mailbox":      {Capability: middleware.CapDomainsWrite, Scope: "mailbox:owner@example.com"},
		"key_wrong_domain": {Capability: middleware.CapDomainsWrite, Scope: "domain:other.test"},
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
		stored, err := models.EncryptDKIMPrivateKey(fixture, signingSetterMasterKey)
		if err != nil {
			t.Fatal("encrypt public signing fixture")
		}
		c := &signingSetterConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}, stored: stored}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return signingSetterTime }
		if err := db.Callback().Query().After("gorm:after_query").Register("signing:cancel-after-lookup", func(tx *gorm.DB) {
			if c.cancel != nil && tx.Statement.Table == "domains" && !strings.HasPrefix(tx.Statement.SQL.String(), `SELECT "name"`) {
				c.cancel()
			}
		}); err != nil {
			t.Fatal(err)
		}
		r := NewRouters(db, jwt, &config.Config{Environment: "test", MasterKey: signingSetterMasterKey, APIMaxBodyBytes: signingSetterBodyLimit, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.reads = 0
			c.current = nil
			c.createdName = ""
			if req.Header.Get("X-Example-Cancel") == "after-signing-lookup" {
				var ctx context.Context
				ctx, c.cancel = context.WithCancel(req.Context())
				defer func() { c.cancel(); c.cancel = nil }()
				req = req.WithContext(ctx)
			}
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
	for op := range signingSetterPaths {
		for name := range pairs[op] {
			out[name] = normal
		}
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.All}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.Domain("example.com")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.Mailbox("owner@example.com")}}, false, false)
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
		limited.handler.ServeHTTP(httptest.NewRecorder(), r.Clone(r.Context()))
		limited.handler.ServeHTTP(w, r)
	}), "rmk_invalid"}, limited.store}
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
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.Domain("other.test")}}, false, false)
	out["scope_lookup_unavailable"] = out["domain_scope"]
	out["scoped_missing"] = out["domain_scope"]
	out["cancelled_after_lookup"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Example-Cancel", "after-signing-lookup")
		normal.handler.ServeHTTP(w, r)
	}), token}, normal.store}
	return out
}

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
func TestSigningSetterLookupCancellationAndConcurrentRemoval(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		status     int
	}{{"lookup", "/api/v1/admin/dkim/3", 500}, {"cancelled", "/api/v1/admin/dkim/1", 500}, {"concurrent", "/api/v1/admin/dkim/6", 404}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSigningSetterScenarios(t)
			s := f["write_only"]
			if tc.name == "cancelled" {
				s = f["cancelled_after_lookup"]
			}
			body := signingSetterBody(t, []byte(`{"selector":"contract","private_key":"fixture-signing-key"}`))
			rr := mailboxSessionEndingRequest(t, s, http.MethodPut, tc.path, body)
			if rr.Code != tc.status {
				t.Fatalf("signing %s got%d want%d", tc.name, rr.Code, tc.status)
			}
		})
	}
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
