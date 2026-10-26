package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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

func (c *signingSetterConnection) Driver() driver.Driver { return c }

func (c *signingSetterConnection) Open(string) (driver.Conn, error) { return c, nil }

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
		t.Cleanup(func() { _ = pool.Close() })
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
	// Explicit fixture cases keep regression tests independent of published examples.
	for _, name := range []string{
		"body_array",
		"body_boundary",
		"cancelled_after_lookup",
		"concurrent",
		"configured",
		"cookie_session",
		"csrf_mismatch",
		"csrf_missing",
		"disabled",
		"domain_scope",
		"field_unknown",
		"installed",
		"interval_missing",
		"interval_negative",
		"interval_null",
		"interval_too_large",
		"interval_wrong_type",
		"invalid_format",
		"invalid_id",
		"key_domain",
		"key_holder_missing",
		"key_mailbox",
		"key_read",
		"key_write",
		"key_wrong_domain",
		"legacy_age_unknown",
		"lookup_unavailable",
		"mailbox_scope",
		"malformed_json",
		"maximum",
		"missing",
		"permission_store_unavailable",
		"private_key_empty",
		"private_key_invalid",
		"private_key_missing",
		"private_key_wrong_type",
		"rate_limited",
		"read_only",
		"replaced",
		"request_too_large",
		"scope_lookup_unavailable",
		"scoped_missing",
		"selector_changed",
		"selector_empty",
		"selector_invalid",
		"selector_missing",
		"selector_wrong_type",
		"session_unavailable",
		"status_unavailable",
		"unauthenticated",
		"unchanged",
		"update_unavailable",
		"write_only",
		"wrong_domain",
	} {
		out[name] = normal
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
