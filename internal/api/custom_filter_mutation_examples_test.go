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
	"path/filepath"
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
)

var customFilterMutationPaths = map[string]string{"createCustomFilter": "/api/v1/admin/custom-filters", "updateCustomFilter": "/api/v1/admin/custom-filters/{id}"}

// Controlled rows run production GORM mapping, credential checks and mutations.
// They prove SQL wiring; the separate scratch-database regression checks persistence.
type customFilterMutationConnection struct {
	accountCredentialListConnection
	store *bulkQueueExampleStore
}

func (c *customFilterMutationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *customFilterMutationConnection) Driver() driver.Driver                        { return c }
func (c *customFilterMutationConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *customFilterMutationConnection) Begin() (driver.Tx, error)                    { return bulkQueueExampleTx{}, nil }
func (c *customFilterMutationConnection) record(q string, args []driver.NamedValue) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
}
func (c *customFilterMutationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "custom_filters"`) {
		c.record(q, args)
		fields, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		if fields["name"] == "WriteFailure" {
			return nil, errors.New("private custom filter insertion detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(q, `FROM "custom_filters"`) {
		c.record(q, args)
		if len(args) != 2 || !strings.Contains(q, `"custom_filters"."id" =`) || args[1].Value != int64(1) {
			return nil, errors.New("custom filter load lost its exact ID or bound")
		}
		id, ok := args[0].Value.(int64)
		if !ok {
			return nil, errors.New("non-numeric bound custom filter ID")
		}
		if id == 3 {
			return nil, errors.New("private custom filter lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "name", "description", "filter_type", "direction", "config", "enabled", "created_at", "updated_at"}}
		if id != 2147483647 {
			r.rows = [][]driver.Value{{id, int64(1), "tag-invoices", "Tags mail about invoices", "action", "inbound", []byte(`{"script":"function filter(email) {\n  return { action: \"continue\" };\n}\n"}`), id != 2, sieveInstallationTime, sieveInstallationTime}}
		}
		return r, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}
func (c *customFilterMutationConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "custom_filters" SET`) {
		return nil, errors.New("unexpected custom filter ancillary write")
	}
	c.record(q, args)
	fields, err := vacationMutationFields(q, args)
	if err != nil {
		return nil, err
	}
	if fields["id"] == int64(4) {
		return nil, errors.New("private custom filter update detail")
	}
	if fields["domain_id"] != int64(1) {
		return nil, errors.New("patch moved custom filter domain")
	}
	return driver.RowsAffected(1), nil
}
func newCustomFilterMutationScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":   {Capability: middleware.CapPipelinesWrite, Scope: "*"},
		"key_read":    {Capability: middleware.CapPipelinesRead, Scope: "*"},
		"key_domain":  {Capability: middleware.CapPipelinesWrite, Scope: "domain:restmail.test"},
		"key_mailbox": {Capability: middleware.CapPipelinesWrite, Scope: "mailbox:owner@restmail.test"},
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
					return grants.Held{}, errors.New("private custom filter grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &customFilterMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return sieveInstallationTime }
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{r.Public, token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	for _, name := range []string{"created", "updated", "enabled_wrong_type", "filter_type_invalid", "direction_invalid", "config_array", "config_null", "name_empty", "missing", "lookup_unavailable", "write_unavailable", "body_array", "malformed_json", "request_too_large", "body_boundary", "required_missing", "config_missing", "config_scalar", "name_wrong_type", "domain_wrong_type", "unknown_field", "nested_domain", "forged_id", "forged_timestamp", "enabled_default", "enabled_true", "enabled_false", "transform_outbound", "nested_config", "unicode", "unchanged", "null_fields", "ignored_domain", "empty_description", "empty_config", "invalid_id", "invalid_format", "invalid_overflow"} {
		out[name] = normal
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapPipelinesWrite, Scope: grants.All}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapPipelinesRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapPipelinesWrite, Scope: grants.Domain("restmail.test")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapPipelinesWrite, Scope: grants.Mailbox("owner@restmail.test")}}, false, false)
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
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "custom-filter-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "custom-filter-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong")
			}
			normal.handler.ServeHTTP(w, r)
		}), ""}, normal.store}
	}
	return out
}
func TestCustomFilterMutationLookupFailureIsNotMissing(t *testing.T) {
	s := newCustomFilterMutationScenarios(t)["updated"]
	r := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/custom-filters/3", strings.NewReader(`{"enabled":false}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+s.token)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, r)
	if rr.Code != 500 {
		t.Fatalf("lookup failure got%d %s want500", rr.Code, rr.Body)
	}
	for _, m := range s.store.snapshot() {
		if !strings.HasPrefix(m.query, "SELECT") {
			t.Fatal("lookup failure wrote a row")
		}
	}
}
func TestCustomFilterMutationExamples(t *testing.T) {
	scenarios := newCustomFilterMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range customFilterMutationPaths {
		if len(pairs[op]) < 30 {
			t.Fatalf("incomplete operation%s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario%s", name)
				}
				method := http.MethodPost
				if op == "updateCustomFilter" {
					method = http.MethodPatch
				}
				body := []byte(pair.Request.Body)
				if name == "malformed_json" {
					body = []byte("{")
				}
				if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 513)
				}
				if name == "body_boundary" {
					if len(body) > 512 {
						t.Fatal("boundary example exceeds the configured cap")
					}
					body = append(body, bytes.Repeat([]byte(" "), 512-len(body))...)
				}
				before := 0
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				r := httptest.NewRequest(method, messageCollectionReadURI(path, pair), bytes.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if s.token != "" {
					r.Header.Set("Authorization", "Bearer "+s.token)
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, r)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				writes := 0
				if s.store != nil {
					for _, m := range s.store.snapshot()[before:] {
						if strings.HasPrefix(m.query, "SELECT") {
							continue
						}
						writes++
						var fields map[string]driver.Value
						var err error
						if method == http.MethodPost {
							fields, err = exampleInsertFields(m.query, m.args)
						} else {
							fields, err = vacationMutationFields(m.query, m.args)
							if fields["id"] != int64(pair.Request.Path["id"].(float64)) {
								t.Fatal("update lost exact row")
							}
						}
						if err != nil {
							t.Fatal(err)
						}
						if fields["domain_id"] != int64(1) {
							t.Fatal("mutation changed domain identity")
						}
						if rr.Code < 300 {
							var answer struct{ Data map[string]interface{} }
							if err := json.Unmarshal(pair.Response.Body, &answer); err != nil {
								t.Fatal(err)
							}
							for _, key := range []string{"name", "description", "filter_type", "direction", "enabled"} {
								if fields[key] != answer.Data[key] {
									t.Fatalf("stored%s=%v want%v", key, fields[key], answer.Data[key])
								}
							}
							var config interface{}
							raw, ok := fields["config"].([]byte)
							if !ok {
								t.Fatalf("config binding has type %T", fields["config"])
							}
							if err := json.Unmarshal(raw, &config); err != nil {
								t.Fatal(err)
							}
							got, _ := json.Marshal(config)
							want, _ := json.Marshal(answer.Data["config"])
							if !bytes.Equal(got, want) {
								t.Fatalf("stored config%s want%s", got, want)
							}
							if fields["created_at"] != sieveInstallationTime || fields["updated_at"] != sieveInstallationTime {
								t.Fatal("lost server timestamps")
							}
						}
					}
				}
				want := rr.Code < 300 || name == "write_unavailable"
				if (writes == 1) != want || writes > 1 {
					t.Fatalf("writes%d want%t", writes, want)
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := customFilterMutationPaths[w.OperationID]; ok {
				t.Errorf("custom filter warning:%v", w)
			}
		}
	}
}
func TestCustomFilterMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for custom filter exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := newCustomFilterMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range customFilterMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario%s", name)
				}
				method := http.MethodPost
				if op == "updateCustomFilter" {
					method = http.MethodPatch
				}
				checkQueueMutationPairWithVertrag(t, binary, s, method, path, name, pair, 513)
			})
		}
	}
}
