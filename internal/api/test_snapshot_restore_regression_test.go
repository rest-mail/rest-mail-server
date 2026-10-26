package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

const testSnapshotRestorePath = "/api/v1/admin/test/snapshot/restore"

type testSnapshotRestoreConnection struct{ testDataMutationConnection }

func (c *testSnapshotRestoreConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *testSnapshotRestoreConnection) Driver() driver.Driver { return c }

func (c *testSnapshotRestoreConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *testSnapshotRestoreConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, "INSERT INTO ") {
		match := testDataInsertTable.FindStringSubmatch(q)
		if len(match) != 2 {
			return nil, errors.New("unrecognized restore insert")
		}
		c.relevant(ctx, q, args)
		if c.mode == "failure_"+match[1] {
			return nil, errors.New("private restore insert detail")
		}
		fields, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		id, ok := fields["id"].(int64)
		if !ok || id == 0 {
			c.nextID++
			id = c.nextID
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{id}}}, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func newTestSnapshotRestoreScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
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
		c := &testSnapshotRestoreConnection{testDataMutationConnection: testDataMutationConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }

		environment := "test"
		if production {
			environment = "production"
		}
		r := NewRouters(db, jwt, &config.Config{Environment: environment, APIMaxBodyBytes: 16384}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.mode = req.Header.Get("X-Fixture-Data-Mode")
			c.nextID = 100
			c.tx = nil
			c.aborted = false
			c.cleared = false
			ctx, cancel := context.WithCancel(context.WithValue(req.Context(), testDataMutationContextKey{}, true))
			defer cancel()
			c.cancel = nil
			if c.mode == "cancelled" {
				c.cancel = cancel
			}
			r.Public.ServeHTTP(w, req.WithContext(ctx))
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	// Explicit fixture cases keep regression tests independent of published examples.
	for _, name := range []string{
		"admin",
		"cancelled",
		"clear_failure",
		"commit_failure",
		"cookie_session",
		"csrf_mismatch",
		"csrf_missing",
		"domain_scope",
		"empty",
		"failure_aliases",
		"failure_bans",
		"failure_domains",
		"failure_mailboxes",
		"failure_messages",
		"failure_outbound_queue",
		"failure_pipelines",
		"key_admin",
		"key_domain",
		"key_domain_read",
		"key_holder_missing",
		"key_mailbox",
		"key_wrong_domain",
		"mailbox_scope",
		"malformed_json",
		"missing_table",
		"non_admin",
		"non_object",
		"permission_store_unavailable",
		"request_too_large",
		"session_unavailable",
		"snapshot",
		"snapshot_associations",
		"sparse_id",
		"unauthenticated",
		"unknown_field",
		"wrong_domain",
	} {
		out[name] = normal
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

	for _, name := range []string{"failure_domains", "failure_mailboxes", "failure_aliases", "failure_messages", "failure_outbound_queue", "failure_pipelines", "failure_bans", "clear_failure", "missing_table", "commit_failure", "cancelled"} {
		name := name
		out[name] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Set("X-Fixture-Data-Mode", name)
			normal.handler.ServeHTTP(w, r)
		}), token}, normal.store}
	}
	out["production"] = makeRouter(wild, false, true)
	return out
}

func TestSnapshotRestoreProductionAbsent(t *testing.T) {
	s := newTestSnapshotRestoreScenarios(t)["production"]
	rr := mailboxSessionEndingRequest(t, s, http.MethodPost, testSnapshotRestorePath, []byte(`{}`))
	if rr.Code != 404 || len(s.store.snapshot()) != 0 {
		t.Fatal("production exposed restore")
	}
}
