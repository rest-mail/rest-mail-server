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

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/gorm"
)

// Controlled SQL proves the address guards and exact domain selection. Removal
// also takes the grant lock, reads the grants held on the domain (none here) and
// clears the configuration the domain owns (#1141); those statements are
// accepted, and the domain's own DELETE stays the one judged.
type domainRemovalConnection struct {
	domainMutationConnection
	scopedGrant bool
}

// The configuration tables removal clears by domain_id, in deletion order.
var domainOwnedTables = []string{"pipelines", "custom_filters", "domain_sender_rules", "certificates", "dkim_rotation_keys", "mtasts_policies", "domain_relays", "suppressions"}

func (c *domainRemovalConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *domainRemovalConnection) Driver() driver.Driver { return c }

func (c *domainRemovalConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *domainRemovalConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	for _, table := range []string{"mailboxes", "aliases"} {
		if strings.HasPrefix(q, `SELECT count(*) FROM "`+table+`"`) {
			c.record(q, args)
			if len(args) != 1 || !strings.Contains(q, "domain_id =") {
				return nil, errors.New("domain address guard lost domain selector")
			}
			id := args[0].Value.(int64)
			if table == "mailboxes" && id == 4 || table == "aliases" && id == 5 {
				return nil, errors.New("private address count detail")
			}
			n := int64(0)
			if table == "mailboxes" && id == 6 || table == "aliases" && id == 7 {
				n = 1
			}
			return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{n}}}, nil
		}
	}
	if strings.HasPrefix(q, `SELECT * FROM "grants" WHERE`) {
		c.record(q, args)
		if c.scopedGrant {
			return importRows(map[string]any{"id": int64(21), "holder_kind": "account", "holder_id": int64(12), "role_id": int64(7), "scope": "domain:example.org", "mode": "active"}), nil
		}
		return &authReadSnapshotRows{columns: []string{"id"}}, nil
	}
	if c.scopedGrant && strings.Contains(q, `FROM "roles"`) {
		return importRows(map[string]any{"id": int64(7), "name": "owner"}), nil
	}
	return c.domainMutationConnection.QueryContext(ctx, q, args)
}

func (c *domainRemovalConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "SELECT pg_advisory_xact_lock(") {
		c.record(q, args)
		return driver.RowsAffected(0), nil
	}
	for _, table := range domainOwnedTables {
		if strings.HasPrefix(q, `DELETE FROM "`+table+`" WHERE`) {
			if len(args) != 1 || !strings.Contains(q, "domain_id =") {
				return nil, errors.New("domain cleanup lost domain selector")
			}
			c.mutation(q, args)
			return driver.RowsAffected(0), nil
		}
	}
	if q != `DELETE FROM "domains" WHERE "domains"."id" = $1` || len(args) != 1 {
		return nil, errors.New("domain removal lost exact selector")
	}
	c.mutation(q, args)
	if args[0].Value == int64(9) {
		return nil, errors.New("private domain deletion detail")
	}
	return driver.RowsAffected(1), nil
}

func newDomainRemovalScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":        {Capability: middleware.CapDomainsDelete, Scope: "*"},
		"key_read":         {Capability: middleware.CapDomainsRead, Scope: "*"},
		"key_domain":       {Capability: middleware.CapDomainsDelete, Scope: "domain:example.org"},
		"key_mailbox":      {Capability: middleware.CapDomainsDelete, Scope: "mailbox:owner@example.org"},
		"key_wrong_domain": {Capability: middleware.CapDomainsDelete, Scope: "domain:other.test"},
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
		c := &domainRemovalConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		var cancel context.CancelFunc
		if err := db.Callback().Query().After("gorm:after_query").Register("example:cancel-domain-removal", func(tx *gorm.DB) {
			if cancel != nil && tx.Statement.Table == "domains" && !strings.HasPrefix(tx.Statement.SQL.String(), `SELECT "name"`) {
				cancel()
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
			c.scopedGrant = req.Header.Get("X-Example-Scoped-Grant") == "true"
			if req.Header.Get("X-Example-Cancel") == "after-domain-lookup" {
				var ctx context.Context
				ctx, cancel = context.WithCancel(req.Context())
				defer func() { cancel(); cancel = nil }()
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
		"account_count_unavailable",
		"alias_count_unavailable",
		"body_boundary",
		"cancelled_after_lookup",
		"cookie_session",
		"csrf_mismatch",
		"csrf_missing",
		"delete_unavailable",
		"deleted",
		"domain_scope",
		"has_accounts",
		"has_aliases",
		"inactive",
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
		"missing",
		"permission_store_unavailable",
		"rate_limited",
		"read_only",
		"request_too_large",
		"scope_lookup_unavailable",
		"scoped_missing",
		"session_unavailable",
		"unauthenticated",
		"write_only",
		"wrong_domain",
	} {
		out[name] = normal
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsDelete, Scope: grants.All}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsDelete, Scope: grants.Domain("example.org")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsDelete, Scope: grants.Mailbox("owner@example.org")}}, false, false)
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
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsDelete, Scope: grants.Domain("other.test")}}, false, false)
	out["scope_lookup_unavailable"] = out["domain_scope"]
	out["scoped_missing"] = out["domain_scope"]
	out["cancelled_after_lookup"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Example-Cancel", "after-domain-lookup")
		normal.handler.ServeHTTP(w, r)
	}), token}, normal.store}
	out["two_factor_required"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Example-Scoped-Grant", "true")
		normal.handler.ServeHTTP(w, r)
	}), token}, normal.store}
	return out
}

func TestDomainRemovalHonorsCancellation(t *testing.T) {
	s := newDomainRemovalScenarios(t)["cancelled_after_lookup"]
	r := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/domains/1", nil)
	r.Header.Set("Authorization", "Bearer "+s.token)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, r)
	if rr.Code != 500 {
		t.Fatalf("cancelled deletion got%d %s want500", rr.Code, rr.Body)
	}
	for _, call := range s.store.snapshot() {
		if strings.HasPrefix(call.query, "DELETE") {
			t.Fatal("cancelled request deleted a domain")
		}
	}
}
