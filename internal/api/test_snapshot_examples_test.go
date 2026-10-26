package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
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

const testSnapshotPath = "/api/v1/admin/test/snapshot"

var testSnapshotTables = []string{"domains", "mailboxes", "aliases", "messages", "outbound_queue", "pipelines", "bans"}

type testSnapshotContextKey struct{}
type testSnapshotConnection struct {
	domainMutationConnection
	failTable string
	empty     bool
	cancel    context.CancelFunc
}

func (c *testSnapshotConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *testSnapshotConnection) Driver() driver.Driver                        { return c }
func (c *testSnapshotConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *testSnapshotConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	for _, table := range testSnapshotTables {
		if q == `SELECT * FROM "`+table+`"` && len(args) == 0 {
			c.record(q, args)
			c.record("SNAPSHOT_CONTEXT", []driver.NamedValue{{Value: ctx.Value(testSnapshotContextKey{}) == true}})
			if c.failTable == table {
				return nil, errors.New("private snapshot dependency detail")
			}
			if c.cancel != nil {
				c.cancel()
				c.cancel = nil
			}
			stamp := sieveInstallationTime
			columns := []string{"id"}
			values := []driver.Value{int64(0)}
			switch table {
			case "domains":
				columns = []string{"id", "name", "server_type", "active", "default_quota_bytes", "dkim_selector", "sending_policy", "dmarc_policy", "created_at", "updated_at"}
				values = []driver.Value{int64(11), "example.com", "traditional", false, int64(0), "rm2026", "bulk", "reject", stamp, stamp}
			case "mailboxes":
				columns = []string{"id", "domain_id", "local_part", "address", "display_name", "quota_bytes", "quota_used_bytes", "active", "created_at", "updated_at"}
				values = []driver.Value{int64(21), int64(11), "owner", "owner@example.com", "Fixture Owner", int64(1073741824), int64(123), false, stamp, stamp}
			case "aliases":
				columns = []string{"id", "domain_id", "source_address", "destination_address", "active", "created_at"}
				values = []driver.Value{int64(31), int64(11), "alias@example.com", "owner@example.com", false, stamp}
			case "messages":
				columns = []string{"id", "mailbox_id", "folder", "message_id", "sender", "recipients_to", "subject", "body_text", "headers", "size_bytes", "is_read", "received_at", "created_at", "updated_at"}
				values = []driver.Value{int64(41), int64(21), "INBOX", "<snapshot-fixture@example.com>", "sender@example.net", []byte(`["owner@example.com"]`), "Fixture subject", "Fixture body ✓", []byte(`{"X-Fixture":"true"}`), int64(123), false, stamp, stamp, stamp}
			case "outbound_queue":
				columns = []string{"id", "message_id", "sender", "recipient", "domain", "status", "attempts", "max_retries", "next_attempt", "created_at", "expires_at"}
				values = []driver.Value{int64(51), int64(41), "sender@example.net", "owner@example.com", "example.com", "pending", int64(0), int64(3), stamp, stamp, stamp.Add(time.Hour)}
			case "pipelines":
				columns = []string{"id", "domain_id", "direction", "filters", "active", "created_at", "updated_at"}
				values = []driver.Value{int64(61), int64(11), "inbound", []byte(`[{"name":"header_validate","enabled":false,"config":{"nested":[1,false]}}]`), false, stamp, stamp}
			case "bans":
				columns = []string{"id", "ip", "reason", "protocol", "created_by", "created_at"}
				values = []driver.Value{int64(71), "192.0.2.17", "Fixture only", "smtp", "fixture", stamp}
			}
			r := &authReadSnapshotRows{columns: columns}
			if !c.empty {
				r.rows = [][]driver.Value{values}
			}
			return r, nil
		}
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}
func (c *testSnapshotConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	for _, table := range testSnapshotTables {
		if strings.Contains(q, `"`+table+`"`) {
			c.record("SNAPSHOT_WRITE", args)
		}
	}
	return nil, errors.New("snapshot must not mutate resources")
}
func newTestSnapshotScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_admin":        {Capability: grants.Wildcard, Scope: "*"},
		"key_domain_read":  {Capability: middleware.CapDomainsRead, Scope: "*"},
		"key_domain":       {Capability: grants.Wildcard, Scope: "domain:example.com"},
		"key_mailbox":      {Capability: grants.Wildcard, Scope: "mailbox:owner@example.com"},
		"key_wrong_domain": {Capability: grants.Wildcard, Scope: "domain:other.test"},
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
	makeRouter := func(entries []grants.Entry, fail, production bool) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if fail {
					return grants.Held{}, errors.New("private snapshot grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &testSnapshotConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }

		environment := "test"
		if production {
			environment = "production"
		}
		r := NewRouters(db, jwt, &config.Config{Environment: environment, APIMaxBodyBytes: 512}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.failTable = req.Header.Get("X-Fixture-Snapshot-Failure")
			c.empty = req.Header.Get("X-Fixture-Snapshot-Empty") == "true"
			ctx, cancel := context.WithCancel(context.WithValue(req.Context(), testSnapshotContextKey{}, true))
			if req.Header.Get("X-Fixture-Snapshot-Cancel") == "true" {
				c.cancel = cancel
			} else {
				c.cancel = nil
			}
			defer func() { cancel(); c.cancel = nil }()
			req = req.WithContext(ctx)
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
	for _, op := range []string{"snapshotTestData"} {
		for name := range pairs[op] {
			out[name] = normal
		}
	}
	out["admin"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}, false, false)
	out["non_admin"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Domain("example.com")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Mailbox("owner@example.com")}}, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false)
	out["unauthenticated"] = bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}
	out["session_unavailable"] = bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}
	for name, key := range tokens {
		s := normal
		s.token = key
		out[name] = s
	}
	missing := out["non_admin"]
	missing.token = tokens["key_admin"]
	out["key_holder_missing"] = missing
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
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Domain("other.test")}}, false, false)

	for _, table := range testSnapshotTables {
		table := table
		out["failure_"+table] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Set("X-Fixture-Snapshot-Failure", table)
			normal.handler.ServeHTTP(w, r)
		}), token}, normal.store}
	}
	for _, name := range []string{"empty", "cancelled"} {
		name := name
		out[name] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			if name == "empty" {
				r.Header.Set("X-Fixture-Snapshot-Empty", "true")
			} else {
				r.Header.Set("X-Fixture-Snapshot-Cancel", "true")
			}
			normal.handler.ServeHTTP(w, r)
		}), token}, normal.store}
	}
	out["snapshot"] = normal
	out["production"] = makeRouter(wild, false, true)
	return out
}

func TestTestSnapshotReadFailures(t *testing.T) {
	f := newTestSnapshotScenarios(t)
	for _, table := range append(append([]string(nil), testSnapshotTables...), "cancelled") {
		t.Run(table, func(t *testing.T) {
			name := "failure_" + table
			if table == "cancelled" {
				name = table
			}
			rr := mailboxSessionEndingRequest(t, f[name], http.MethodPost, testSnapshotPath, nil)
			if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
				t.Fatalf("snapshot failure got%d %s want sanitized500", rr.Code, rr.Body)
			}
		})
	}
}
func TestTestSnapshotContainsEverySelectedTable(t *testing.T) {
	f := newTestSnapshotScenarios(t)
	rr := mailboxSessionEndingRequest(t, f["snapshot"], http.MethodPost, testSnapshotPath, nil)
	if rr.Code != 200 {
		t.Fatalf("snapshot got%d %s", rr.Code, rr.Body)
	}
	var result struct{ Data map[string]json.RawMessage }
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for key, id := range map[string]int{"domains": 11, "mailboxes": 21, "aliases": 31, "messages": 41, "queue": 51, "pipelines": 61, "bans": 71} {
		var rows []struct{ ID int }
		if json.Unmarshal(result.Data[key], &rows) != nil || len(rows) != 1 || rows[0].ID != id {
			t.Fatalf("snapshot lost selected table%s", key)
		}
	}

	var values map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	data := values["data"].(map[string]interface{})
	expected := map[string]map[string]interface{}{
		"domains":   {"name": "example.com", "active": false, "default_quota_bytes": float64(0)},
		"mailboxes": {"domain_id": float64(11), "address": "owner@example.com", "quota_used_bytes": float64(123), "active": false},
		"aliases":   {"source_address": "alias@example.com", "destination_address": "owner@example.com", "active": false},
		"messages":  {"mailbox_id": float64(21), "message_id": "<snapshot-fixture@example.com>", "subject": "Fixture subject", "body_text": "Fixture body ✓", "is_read": false},
		"queue":     {"message_id": float64(41), "status": "pending", "attempts": float64(0), "max_retries": float64(3)},
		"pipelines": {"direction": "inbound", "active": false, "domain_id": float64(11)},
		"bans":      {"ip": "192.0.2.17", "reason": "Fixture only", "protocol": "smtp"},
	}
	for table, fields := range expected {
		row := data[table].([]interface{})[0].(map[string]interface{})
		for field, want := range fields {
			if row[field] != want {
				t.Fatalf("snapshot%s.%s got%v want%v", table, field, row[field], want)
			}
		}
	}
	if strings.Contains(rr.Body.String(), "password_hash") || strings.Contains(rr.Body.String(), "dkim_private_key") {
		t.Fatal("snapshot crossed the existing model JSON credential boundary")
	}
	if output := os.Getenv("RESTMAIL_SNAPSHOT_CAPTURE"); output != "" {
		if err := os.WriteFile(output, rr.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertTestSnapshotBody(t *testing.T, wantRaw json.RawMessage, gotRaw []byte, start, end time.Time) {
	t.Helper()
	var want, got map[string]interface{}
	if json.Unmarshal(wantRaw, &want) != nil || json.Unmarshal(gotRaw, &got) != nil {
		t.Fatal("snapshot response is not JSON")
	}
	if data, ok := got["data"].(map[string]interface{}); ok {
		raw, ok := data["snapshot_at"].(string)
		stamp, err := time.Parse(time.RFC3339Nano, raw)
		if !ok || err != nil || stamp.Before(start.Add(-time.Second)) || stamp.After(end.Add(time.Second)) {
			t.Fatal("snapshot time is absent, invalid or outside the request")
		}
		want["data"].(map[string]interface{})["snapshot_at"] = raw
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicExampleBody(t, encoded, gotRaw)
}
func TestTestSnapshotExamples(t *testing.T) {
	f := newTestSnapshotScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["snapshotTestData"]) < 20 {
		t.Fatal("snapshot examples incomplete")
	}
	for name, pair := range pairs["snapshotTestData"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing snapshot scenario%s", name)
			}
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			var body []byte
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			start := time.Now()
			rr := mailboxSessionEndingRequest(t, s, http.MethodPost, testSnapshotPath, body)
			end := time.Now()
			if rr.Code != pair.Response.Status {
				t.Fatalf("snapshot got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			assertTestSnapshotBody(t, pair.Response.Body, rr.Body.Bytes(), start, end)
			selected := []string{}
			if s.store != nil {
				for _, c := range s.store.snapshot()[before:] {
					if c.query == "SNAPSHOT_WRITE" {
						t.Fatal("snapshot attempted to mutate resources")
					}
					if c.query == "SNAPSHOT_CONTEXT" && c.args[0].Value != true {
						t.Fatal("snapshot query lost HTTP context")
					}
					if strings.HasPrefix(c.query, "SELECT *") {
						selected = append(selected, c.query)
					}
				}
			}
			want := []string{}
			if rr.Code == 200 {
				for _, table := range testSnapshotTables {
					want = append(want, `SELECT * FROM "`+table+`"`)
				}
			}
			if strings.HasPrefix(name, "failure_") {
				for _, table := range testSnapshotTables {
					want = append(want, `SELECT * FROM "`+table+`"`)
					if "failure_"+table == name {
						break
					}
				}
			}
			if name == "cancelled" {
				want = []string{`SELECT * FROM "domains"`}
			}
			a, _ := json.Marshal(selected)
			b, _ := json.Marshal(want)
			if !bytes.Equal(a, b) {
				t.Fatalf("snapshot selects%s want%s", a, b)
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "snapshotTestData" && gapWarningApplies(map[string]gapOperation{"snapshotTestData": {}}, w) {
				t.Errorf("snapshot warning:%v", w)
			}
		}
	}
}
func TestTestSnapshotExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newTestSnapshotScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["snapshotTestData"] {
		t.Run(name, func(t *testing.T) {
			checkQueueMutationPairWithVertrag(t, binary, f[name], http.MethodPost, testSnapshotPath, name, pair, 513)
		})
	}
}

func TestTestSnapshotProductionRouteAbsent(t *testing.T) {
	s := newTestSnapshotScenarios(t)["production"]
	rr := mailboxSessionEndingRequest(t, s, http.MethodPost, testSnapshotPath, nil)
	if rr.Code != 404 || len(s.store.snapshot()) != 0 {
		t.Fatalf("production exposed a snapshot handler: status%d resource queries%d", rr.Code, len(s.store.snapshot()))
	}
}
