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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/internal/pipeline"
)

var domainMutationPaths = map[string]string{"createDomain": "/api/v1/admin/domains", "updateDomain": "/api/v1/admin/domains/{id}"}

// SQL fixtures prove transaction and selection wiring. The scratch-database
// regression proves that a failed default pipeline leaves no partial domain.
type domainMutationConnection struct {
	accountCredentialListConnection
	store       *bulkQueueExampleStore
	currentTx   *domainMutationTx
	reads       int
	current     map[string]driver.Value
	createdName string
}

func (c *domainMutationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *domainMutationConnection) Driver() driver.Driver                        { return c }
func (c *domainMutationConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *domainMutationConnection) record(q string, args []driver.NamedValue) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
}

type domainMutationTx struct {
	c        *domainMutationConnection
	relevant bool
}

func (c *domainMutationConnection) Begin() (driver.Tx, error) {
	tx := &domainMutationTx{c: c}
	c.currentTx = tx
	return tx, nil
}
func (tx *domainMutationTx) Commit() error {
	if tx.relevant {
		tx.c.record("COMMIT", nil)
	}
	tx.c.currentTx = nil
	return nil
}
func (tx *domainMutationTx) Rollback() error {
	if tx.relevant {
		tx.c.record("ROLLBACK", nil)
	}
	tx.c.currentTx = nil
	return nil
}
func (c *domainMutationConnection) mutation(q string, args []driver.NamedValue) {
	if c.currentTx != nil && !c.currentTx.relevant {
		c.record("BEGIN", nil)
		c.currentTx.relevant = true
	}
	c.record(q, args)
}
func (c *domainMutationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "domains"`) {
		c.mutation(q, args)
		f, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		c.createdName = f["name"].(string)
		if c.createdName == "duplicate.test" {
			return nil, &pgconn.PgError{Code: "23505", Message: "private duplicate domain detail"}
		}
		if c.createdName == "write-failure.test" {
			return nil, errors.New("private domain insert detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.HasPrefix(q, `INSERT INTO "pipelines"`) {
		c.mutation(q, args)
		f, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		if c.createdName == "inbound-failure.test" && f["direction"] == "inbound" || c.createdName == "outbound-failure.test" && f["direction"] == "outbound" {
			return nil, errors.New("private default pipeline insert detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(q, `FROM "domains"`) {
		c.record(q, args)
		if strings.HasPrefix(q, `SELECT "name"`) {
			if len(args) != 1 || !strings.Contains(q, "id =") {
				return nil, errors.New("domain scope lost exact ID")
			}
			if args[0].Value == int64(8) {
				return nil, errors.New("private domain scope detail")
			}
			r := &authReadSnapshotRows{columns: []string{"name"}}
			if args[0].Value != int64(2147483647) {
				r.rows = [][]driver.Value{{"example.org"}}
			}
			return r, nil
		}
		if len(args) != 2 || !strings.Contains(q, `"domains"."id" =`) || args[1].Value != int64(1) {
			return nil, errors.New("domain load lost exact ID or bound")
		}
		id := args[0].Value.(int64)
		c.reads++
		if id == 3 || id == 4 && c.reads > 1 {
			return nil, errors.New("private domain lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "name", "server_type", "active", "default_quota_bytes", "dkim_selector", "dkim_private_key", "sending_policy", "dmarc_policy", "created_at", "updated_at"}}
		if id == 2147483647 || id == 5 && c.reads > 1 {
			return r, nil
		}
		if c.current == nil {
			c.current = map[string]driver.Value{"id": id, "name": "example.org", "server_type": "restmail", "active": id == 2, "default_quota_bytes": int64(0), "dkim_selector": "", "sending_policy": "", "dmarc_policy": "none", "created_at": sieveInstallationTime, "updated_at": sieveInstallationTime}
		}
		f := c.current
		r.rows = [][]driver.Value{{f["id"], f["name"], f["server_type"], f["active"], f["default_quota_bytes"], f["dkim_selector"], "private-domain-fixture", f["sending_policy"], f["dmarc_policy"], f["created_at"], f["updated_at"]}}
		return r, nil
	}
	if strings.Contains(q, `FROM "certificates"`) {
		c.record(q, args)
		if len(args) != 1 || !strings.Contains(q, "domain_id =") || !strings.Contains(q, "not_before <= NOW() AND not_after > NOW()") {
			return nil, errors.New("certificate gate lost domain or validity window")
		}
		id := args[0].Value.(int64)
		if id == 7 {
			return nil, errors.New("private certificate count detail")
		}
		n := int64(1)
		if id == 6 {
			n = 0
		}
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{n}}}, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}
func (c *domainMutationConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "domains" SET`) {
		return nil, errors.New("unexpected ancillary domain write")
	}
	c.mutation(q, args)
	f, err := vacationMutationFields(q, args)
	if err != nil {
		return nil, err
	}
	if c.current == nil || f["id"] != c.current["id"] {
		return nil, errors.New("domain update changed row selector")
	}
	if f["id"] == int64(9) {
		return nil, errors.New("private domain update detail")
	}
	for k, v := range f {
		if k != "id" {
			c.current[k] = v
		}
	}
	return driver.RowsAffected(1), nil
}
func newDomainMutationScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":        {Capability: middleware.CapDomainsWrite, Scope: "*"},
		"key_read":         {Capability: middleware.CapDomainsRead, Scope: "*"},
		"key_domain":       {Capability: middleware.CapDomainsWrite, Scope: "domain:example.org"},
		"key_mailbox":      {Capability: middleware.CapDomainsWrite, Scope: "mailbox:owner@example.org"},
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
		c := &domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return sieveInstallationTime }
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.reads = 0
			c.current = nil
			c.createdName = ""
			r.Public.ServeHTTP(w, req)
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	for _, name := range []string{"body_array", "created", "name_missing", "sending_policy_invalid", "server_type_invalid", "updated", "missing", "default_values", "server_null", "server_empty", "quota_zero", "quota_custom", "idna", "canonical_name", "dmarc_reject", "bulk_policy", "duplicate", "write_unavailable", "inbound_unavailable", "outbound_unavailable", "lookup_unavailable", "reload_unavailable", "reload_missing", "update_unavailable", "activate", "certificate_missing", "certificate_unavailable", "deactivate", "quota_zero_patch", "clear_sending_policy", "dmarc_quarantine", "unchanged", "null_fields", "invalid_name", "name_wrong_type", "dmarc_invalid", "unknown_field", "nested_mailboxes", "forged_id", "forged_key", "invalid_id", "invalid_format", "malformed_json", "request_too_large", "body_boundary"} {
		out[name] = normal
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.All}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.Domain("example.org")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.Mailbox("owner@example.org")}}, false, false)
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
	out["quota_null"] = normal
	out["active_field"] = normal
	return out
}

func TestDomainMutationCreateFailuresAndZeroQuota(t *testing.T) {
	s := newDomainMutationScenarios(t)["created"]
	for _, name := range []string{"write-failure.test", "inbound-failure.test", "outbound-failure.test"} {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]interface{}{"name": name})
			r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/domains", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, r)
			if rr.Code != 500 {
				t.Fatalf("failed domain provisioning got%d %s want500", rr.Code, rr.Body)
			}
		})
	}
	t.Run("explicit zero quota", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/domains", strings.NewReader(`{"name":"zero-quota.test","default_quota_bytes":0}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+s.token)
		rr := httptest.NewRecorder()
		s.handler.ServeHTTP(rr, r)
		var a struct {
			Data struct {
				Quota int64 `json:"default_quota_bytes"`
			}
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
			t.Fatal(err)
		}
		if rr.Code != 201 || a.Data.Quota != 0 {
			t.Fatalf("zero quota got%d %s", rr.Code, rr.Body)
		}
	})
}
func TestDomainMutationExamples(t *testing.T) {
	f := newDomainMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range domainMutationPaths {
		if len(pairs[op]) < 30 {
			t.Fatalf("incomplete operation%s", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f[name]
				if !ok {
					t.Fatalf("missing scenario%s", name)
				}
				method := http.MethodPost
				if op == "updateDomain" {
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
						t.Fatal("oversized boundary")
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
				var calls []bulkQueueExampleMutation
				if s.store != nil {
					calls = s.store.snapshot()[before:]
				}
				var writes []bulkQueueExampleMutation
				begins, commits, rollbacks := 0, 0, 0
				for _, m := range calls {
					switch m.query {
					case "BEGIN":
						begins++
					case "COMMIT":
						commits++
					case "ROLLBACK":
						rollbacks++
					default:
						if !strings.HasPrefix(m.query, "SELECT") {
							writes = append(writes, m)
						}
					}
				}
				if op == "createDomain" {
					wantWrites := 0
					wantCommit := 0
					wantRollback := 0
					if rr.Code == 201 {
						wantWrites = 3
						wantCommit = 1
					}
					if name == "duplicate" || name == "write_unavailable" {
						wantWrites = 1
						wantRollback = 1
					}
					if name == "inbound_unavailable" {
						wantWrites = 2
						wantRollback = 1
					}
					if name == "outbound_unavailable" {
						wantWrites = 3
						wantRollback = 1
					}
					if len(writes) != wantWrites || commits != wantCommit || rollbacks != wantRollback || begins != wantCommit+wantRollback {
						t.Fatalf("writes/begin/commit/rollback %d/%d/%d/%d want%d/%d/%d/%d", len(writes), begins, commits, rollbacks, wantWrites, wantCommit+wantRollback, wantCommit, wantRollback)
					}
					if rr.Code == 201 {
						var answer struct{ Data map[string]interface{} }
						if err := json.Unmarshal(pair.Response.Body, &answer); err != nil {
							t.Fatal(err)
						}
						fields, err := exampleInsertFields(writes[0].query, writes[0].args)
						if err != nil {
							t.Fatal(err)
						}
						if fields["name"] != answer.Data["name"] || fields["server_type"] != answer.Data["server_type"] || fields["active"] != false || fields["default_quota_bytes"] != int64(answer.Data["default_quota_bytes"].(float64)) || fields["sending_policy"] != answer.Data["sending_policy"] || fields["dmarc_policy"] != answer.Data["dmarc_policy"] || fields["created_at"] != sieveInstallationTime || fields["updated_at"] != sieveInstallationTime {
							t.Fatal("created domain fields differ from full answer")
						}
						for i, direction := range []string{"inbound", "outbound"} {
							fields, err := exampleInsertFields(writes[i+1].query, writes[i+1].args)
							if err != nil {
								t.Fatal(err)
							}
							if fields["domain_id"] != int64(1) || fields["direction"] != direction || fields["active"] != true {
								t.Fatal("default pipeline lost domain/direction/active")
							}
							raw, ok := fields["filters"].([]byte)
							if !ok {
								t.Fatalf("pipeline filters type%T", fields["filters"])
							}
							var got, want interface{}
							if err := json.Unmarshal(raw, &got); err != nil {
								t.Fatal(err)
							}
							if err := json.Unmarshal(pipeline.DefaultPipelineJSON(direction), &want); err != nil {
								t.Fatal(err)
							}
							a, _ := json.Marshal(got)
							b, _ := json.Marshal(want)
							if !bytes.Equal(a, b) {
								t.Fatal("default pipeline content differs")
							}
						}
					}
				} else {
					want := rr.Code == 200 && name != "unchanged" && name != "null_fields" || name == "update_unavailable" || name == "reload_unavailable" || name == "reload_missing"
					if (len(writes) == 1) != want || len(writes) > 1 {
						t.Fatalf("update writes%d want%t", len(writes), want)
					}
					if len(writes) == 1 {
						fields, err := vacationMutationFields(writes[0].query, writes[0].args)
						if err != nil {
							t.Fatal(err)
						}
						if fields["id"] != int64(pair.Request.Path["id"].(float64)) {
							t.Fatal("update changed domain identity")
						}
						if _, ok := fields["name"]; ok {
							t.Fatal("update renamed domain")
						}
						if _, ok := fields["dkim_private_key"]; ok {
							t.Fatal("update changed private key")
						}
						if rr.Code == 200 {
							var a struct{ Data map[string]interface{} }
							if err := json.Unmarshal(pair.Response.Body, &a); err != nil {
								t.Fatal(err)
							}
							for k, v := range fields {
								if k == "id" || k == "updated_at" {
									continue
								}
								expected := a.Data[k]
								if k == "default_quota_bytes" {
									expected = int64(expected.(float64))
								}
								if v != expected {
									t.Fatalf("updated%s=%v want%v", k, v, expected)
								}
							}
						}
					}
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := domainMutationPaths[w.OperationID]; ok {
				t.Errorf("domain mutation warning:%v", w)
			}
		}
	}
}
func TestDomainMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for domain mutation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newDomainMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range domainMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f[name]
				if !ok {
					t.Fatalf("missing scenario%s", name)
				}
				method := http.MethodPost
				if op == "updateDomain" {
					method = http.MethodPatch
				}
				checkQueueMutationPairWithVertrag(t, binary, s, method, path, name, pair, 513)
			})
		}
	}
}
