package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

type aliasMutationConnection struct {
	accountCredentialListConnection
	store   *bulkQueueExampleStore
	phase   int
	current map[string]driver.Value
}

func (c *aliasMutationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *aliasMutationConnection) Driver() driver.Driver { return c }

func (c *aliasMutationConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *aliasMutationConnection) Begin() (driver.Tx, error) { return bulkQueueExampleTx{}, nil }

func (c *aliasMutationConnection) record(q string, args []driver.NamedValue) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
}

func (c *aliasMutationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "aliases"`) {
		c.record(q, args)
		fields, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		if fields["source_address"] == "duplicate@restmail.test" {
			return nil, &pgconn.PgError{Code: "23505", Message: "private duplicate alias detail"}
		}
		if fields["source_address"] == "write-failure@restmail.test" {
			return nil, errors.New("private alias insert detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(q, `FROM "aliases"`) {
		c.record(q, args)
		if strings.Contains(q, "JOIN domains") {
			if len(args) != 1 || !strings.Contains(q, "aliases.id =") {
				return nil, errors.New("alias scope lost source identity")
			}
			if args[0].Value == int64(8) {
				return nil, errors.New("private source scope detail")
			}
			r := &authReadSnapshotRows{columns: []string{"name"}}
			if args[0].Value != int64(2147483647) {
				r.rows = [][]driver.Value{{"restmail.test"}}
			}
			return r, nil
		}
		if len(args) < 2 || !strings.Contains(q, `"aliases"."id" =`) || args[len(args)-1].Value != int64(1) {
			return nil, errors.New("alias load lost exact row or bound")
		}
		id := args[0].Value.(int64)
		c.phase++
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "source_address", "destination_address", "active", "created_at"}}
		if c.phase == 1 {
			if id == 3 {
				return nil, errors.New("private alias lookup detail")
			}
			if id == 2147483647 {
				return r, nil
			}
			domain := int64(1)
			if id == 4 {
				domain = 4
			}
			c.current = map[string]driver.Value{"id": id, "domain_id": domain, "source_address": "sales@restmail.test", "destination_address": "old@destination.test", "active": id != 2, "created_at": sieveInstallationTime}
		} else {
			if id == 5 {
				return nil, errors.New("private alias reload detail")
			}
			if id == 6 {
				return r, nil
			}
		}
		if c.current == nil {
			return nil, errors.New("alias reload without selected row")
		}
		r.rows = [][]driver.Value{{c.current["id"], c.current["domain_id"], c.current["source_address"], c.current["destination_address"], c.current["active"], c.current["created_at"]}}
		return r, nil
	}
	if strings.Contains(q, `FROM "domains"`) {
		c.record(q, args)
		if len(args) < 1 {
			return nil, errors.New("domain load lost bound")
		}
		name := "restmail.test"
		if strings.Contains(q, "name =") {
			name = args[0].Value.(string)
			if name == "db-failure.test" {
				return nil, errors.New("private alias source-domain lookup detail")
			}
		} else if args[0].Value == int64(4) {
			return nil, errors.New("private alias domain preload detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "name", "server_type", "active", "default_quota_bytes", "dkim_selector", "sending_policy", "dmarc_policy", "created_at", "updated_at"}}
		if name != "missing.test" {
			r.rows = [][]driver.Value{{int64(1), name, "traditional", true, int64(1073741824), "restmail", "bulk", "reject", sieveInstallationTime, sieveInstallationTime}}
		}
		return r, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func (c *aliasMutationConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "aliases" SET`) {
		return nil, errors.New("unexpected ancillary alias write")
	}
	fields, err := vacationMutationFields(q, args)
	if err != nil {
		return nil, err
	}
	c.record(q, args)
	if c.current == nil || fields["id"] != c.current["id"] {
		return nil, errors.New("alias save lost loaded primary key")
	}
	if fields["id"] == int64(7) {
		return nil, errors.New("private alias update detail")
	}
	for k, v := range fields {
		if k != "id" && k != "active" && k != "destination_address" {
			return nil, fmt.Errorf("alias changed immutable field:%s", k)
		}
		c.current[k] = v
	}
	return driver.RowsAffected(1), nil
}

func newAliasMutationScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{"key_write": {Capability: middleware.CapMailboxesWrite, Scope: "domain:restmail.test"}, "key_read": {Capability: middleware.CapMailboxesRead, Scope: "domain:restmail.test"}, "key_wrong_domain": {Capability: middleware.CapMailboxesWrite, Scope: "domain:other.test"}, "key_mailbox": {Capability: middleware.CapMailboxesWrite, Scope: "mailbox:sales@restmail.test"}} {
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
	makeRouter := func(entries []grants.Entry, permissionFail, limited bool) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if permissionFail {
					return grants.Held{}, errors.New("private alias grant-store detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &aliasMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return sieveInstallationTime }
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.phase = 0
			c.current = nil
			r.Public.ServeHTTP(w, req)
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	for _, n := range []string{"body_array", "created", "source_invalid", "source_missing", "source_wrong_type", "destination_invalid", "missing", "updated", "found", "domain_missing", "domain_unavailable", "duplicate", "write_unavailable", "lookup_unavailable", "reload_unavailable", "reload_missing", "domain_preload_unavailable", "update_unavailable", "unchanged", "null_fields", "inactive", "source_display", "source_idna", "destination_case", "invalid_id", "invalid_format", "request_too_large", "source_crlf", "destination_missing", "destination_empty", "invalid_source_domain", "malformed_json"} {
		out[n] = normal
	}
	out["destination_display"] = normal
	writeOnly := makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesWrite, Scope: grants.All}}, false, false)
	out["updated"] = writeOnly
	out["write_only"] = writeOnly
	scoped := makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesWrite, Scope: grants.Domain("restmail.test")}}, false, false)
	out["source_domain"] = scoped
	out["scope_lookup_unavailable"] = scoped
	out["scoped_missing"] = scoped
	out["domain_read"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesWrite, Scope: grants.Domain("restmail.test")}, {Capability: middleware.CapDomainsRead, Scope: grants.Domain("restmail.test")}}, false, false)
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesWrite, Scope: grants.Domain("other.test")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesWrite, Scope: grants.Mailbox("sales@restmail.test")}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesRead, Scope: grants.All}}, false, false)
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
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "alias-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "alias-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong")
			}
			normal.handler.ServeHTTP(w, r)
		}), ""}, normal.store}
	}
	return out
}

func TestAliasMutationFailuresAreNotMissingConflictsOrSuccess(t *testing.T) {
	f := newAliasMutationScenarios(t)
	s := f["found"]
	for _, tc := range []struct {
		method, uri, body string
		status            int
	}{{"POST", "/api/v1/admin/aliases", `{"source_address":"new@db-failure.test","destination_address":"dest@example.com"}`, 500}, {"POST", "/api/v1/admin/aliases", `{"source_address":"write-failure@restmail.test","destination_address":"dest@example.com"}`, 500}, {"PATCH", "/api/v1/admin/aliases/3", `{"active":false}`, 500}, {"PATCH", "/api/v1/admin/aliases/5", `{"active":false}`, 500}, {"PATCH", "/api/v1/admin/aliases/6", `{"active":false}`, 404}, {"PATCH", "/api/v1/admin/aliases/4", `{"active":false}`, 500}} {
		t.Run(tc.uri+tc.body, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.uri, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+s.token)
			r.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, r)
			if rr.Code != tc.status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, tc.status)
			}
		})
	}
}
