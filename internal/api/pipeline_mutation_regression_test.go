package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

var pipelineMutationTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type pipelineMutationConnection struct {
	domainMutationConnection
	target int64
	cancel context.CancelFunc
}

func (c *pipelineMutationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *pipelineMutationConnection) Driver() driver.Driver { return c }

func (c *pipelineMutationConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *pipelineMutationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "pipelines"`) {
		c.mutation(q, args)
		f, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		if f["domain_id"] == int64(4) {
			return nil, errors.New("private pipeline create detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(q, `FROM "pipelines"`) {
		c.record(q, args)
		if len(args) != 2 || !strings.Contains(q, `"pipelines"."id" =`) || args[1].Value != int64(1) {
			return nil, errors.New("pipeline lookup lost ID or bound")
		}
		c.target = args[0].Value.(int64)
		if c.target == 3 {
			return nil, errors.New("private pipeline lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "direction", "filters", "active", "created_at", "updated_at"}}
		if c.target == 2147483647 {
			return r, nil
		}
		r.rows = [][]driver.Value{{c.target, int64(1), "inbound", []byte(`[{"name":"header_validate","config":{}}]`), true, pipelineMutationTime, pipelineMutationTime}}
		return r, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func (c *pipelineMutationConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "pipelines" SET`) {
		return nil, errors.New("unexpected ancillary pipeline write")
	}
	c.mutation(q, args)
	f, err := vacationMutationFields(q, args)
	if err != nil {
		return nil, err
	}
	if f["id"] != c.target {
		return nil, errors.New("pipeline update lost ID")
	}
	if c.target == 4 {
		return nil, errors.New("private pipeline update detail")
	}
	if c.target == 6 {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

func newPipelineMutationScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
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
		c := &pipelineMutationConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }
		if err := db.Callback().Query().After("gorm:after_query").Register("pipeline:cancel-after-lookup", func(tx *gorm.DB) {
			if c.cancel != nil && tx.Statement.Table == "pipelines" {
				c.cancel()
			}
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Callback().Create().Before("gorm:create").Register("pipeline:cancel-before-create", func(tx *gorm.DB) {
			if c.cancel != nil && tx.Statement.Table == "pipelines" {
				c.cancel()
			}
		}); err != nil {
			t.Fatal(err)
		}
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
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
			r.Public.ServeHTTP(w, req)
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	// Explicit fixture cases keep regression tests independent of published examples.
	for _, name := range []string{
		"active_null",
		"active_only",
		"active_wrong_type",
		"body_array",
		"body_boundary",
		"cancelled_after_lookup",
		"concurrent",
		"cookie_session",
		"created",
		"csrf_mismatch",
		"csrf_missing",
		"direction_missing",
		"disabled",
		"domain_scope",
		"duplicate_unsupported",
		"empty_filters",
		"filters_missing",
		"filters_null",
		"filters_object",
		"global_pipeline",
		"insert_unavailable",
		"invalid_format",
		"invalid_id",
		"key_domain",
		"key_holder_missing",
		"key_mailbox",
		"key_read",
		"key_write",
		"key_wrong_domain",
		"lookup_unavailable",
		"mailbox_scope",
		"malformed_json",
		"missing",
		"null_body",
		"outbound",
		"permission_store_unavailable",
		"rate_limited",
		"read_only",
		"request_too_large",
		"session_unavailable",
		"sieve_valid",
		"unauthenticated",
		"unchanged",
		"update_unavailable",
		"updated",
		"write_only",
		"wrong_domain",
	} {
		out[name] = normal
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
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: middleware.CapPipelinesWrite, Scope: grants.Domain("other.test")}}, false, false)

	out["cancelled_after_lookup"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Example-Cancel", "after-pipeline-lookup")
		normal.handler.ServeHTTP(w, r)
	}), token}, normal.store}
	return out
}

func TestPipelineMutationLookupCancellationAndRemoval(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{{"lookup", "PATCH", "/api/v1/admin/pipelines/3", `{"active":false}`, 500}, {"cancelled_update", "PATCH", "/api/v1/admin/pipelines/1", `{"active":false}`, 500}, {"concurrent", "PATCH", "/api/v1/admin/pipelines/6", `{"active":false}`, 404}, {"cancelled_create", "POST", "/api/v1/admin/pipelines", `{"domain_id":1,"direction":"inbound","filters":[]}`, 500}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPipelineMutationScenarios(t)
			s := f["write_only"]
			if strings.HasPrefix(tc.name, "cancelled") {
				s = f["cancelled_after_lookup"]
			}
			rr := mailboxSessionEndingRequest(t, s, tc.method, tc.path, []byte(tc.body))
			if rr.Code != tc.status {
				t.Fatalf("got%d want%d", rr.Code, tc.status)
			}
		})
	}
}
