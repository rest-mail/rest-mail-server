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

// SQL fixtures prove exact revocation/grant-policy wiring; PostgreSQL tests
// prove persistence/rollback. The ordinary fixture does not emulate SQL durability.
type mailboxRemovalConnection struct {
	accountCredentialListConnection
	store   *bulkQueueExampleStore
	tx      *mailboxRemovalTx
	target  int64
	deleted map[int64]bool
}

func (c *mailboxRemovalConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *mailboxRemovalConnection) Driver() driver.Driver { return c }

func (c *mailboxRemovalConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *mailboxRemovalConnection) record(q string, args []driver.NamedValue) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
}

type mailboxRemovalTx struct {
	c                 *mailboxRemovalConnection
	relevant, removed bool
}

func (c *mailboxRemovalConnection) Begin() (driver.Tx, error) {
	tx := &mailboxRemovalTx{c: c}
	c.tx = tx
	return tx, nil
}

func (tx *mailboxRemovalTx) Commit() error {
	if tx.relevant {
		tx.c.record("COMMIT", nil)
		if tx.removed && tx.c.target == 10 {
			tx.c.deleted[10] = true
		}
	}
	tx.c.tx = nil
	return nil
}

func (tx *mailboxRemovalTx) Rollback() error {
	if tx.relevant {
		tx.c.record("ROLLBACK", nil)
	}
	tx.c.tx = nil
	return nil
}

func (c *mailboxRemovalConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, `FROM "mailboxes"`) {
		if strings.HasPrefix(q, `SELECT "active"`) {
			return &authReadSnapshotRows{columns: []string{"active"}, rows: [][]driver.Value{{true}}}, nil
		}
		if strings.HasPrefix(q, `SELECT "address"`) {
			if strings.Contains(q, "AND active") {
				return c.accountCredentialListConnection.QueryContext(ctx, q, args)
			}
			c.record(q, args)
			if len(args) != 1 || !strings.Contains(q, "id =") {
				return nil, errors.New("mailbox scope lost ID")
			}
			if args[0].Value == int64(8) {
				return nil, errors.New("private mailbox scope detail")
			}
			r := &authReadSnapshotRows{columns: []string{"address"}}
			if args[0].Value != int64(2147483647) {
				r.rows = [][]driver.Value{{"owner@example.org"}}
			}
			return r, nil
		}
		if strings.Contains(q, `"credential_epoch"`) {
			id := args[0].Value.(int64)
			r := &authReadSnapshotRows{columns: []string{"id", "active", "credential_epoch", "password_change_required", "bootstrap_expires_at"}}
			if !c.deleted[id] {
				r.rows = [][]driver.Value{{id, true, int64(0), false, nil}}
			}
			return r, nil
		}
		if strings.HasPrefix(q, `SELECT *`) {
			c.record(q, args)
			if len(args) != 2 || !strings.Contains(q, `"mailboxes"."id" =`) || args[1].Value != int64(1) {
				return nil, errors.New("mailbox load lost ID/bound")
			}
			id := args[0].Value.(int64)
			c.target = id
			if id == 3 {
				return nil, errors.New("private mailbox load detail")
			}
			r := &authReadSnapshotRows{columns: []string{"id", "address", "active"}}
			if id != 2147483647 && !c.deleted[id] {
				r.rows = [][]driver.Value{{id, "owner@example.org", id != 2}}
			}
			return r, nil
		}
	}
	if strings.HasPrefix(q, `UPDATE "credentials"`) {
		c.record(q, args)
		if len(args) == 0 {
			return nil, errors.New("credential revocation lost timestamp")
		}
		stamp, ok := args[0].Value.(time.Time)
		if !ok || stamp.Before(time.Now().Add(-time.Minute)) || stamp.After(time.Now()) {
			return nil, errors.New("credential revocation lost request time")
		}
		if !strings.Contains(q, "account_id =") || !strings.Contains(q, "kind IN") || !strings.Contains(q, "revoked_at IS NULL") || !strings.Contains(q, "RETURNING") {
			return nil, errors.New("credential revocation lost selector/returning")
		}
		if c.target == 5 {
			return nil, errors.New("private credential revocation detail")
		}
		return &authReadSnapshotRows{columns: []string{"id"}}, nil
	}
	if strings.Contains(q, `FROM "grants"`) {
		c.record(q, args)
		r := &authReadSnapshotRows{columns: []string{"id", "holder_kind", "holder_id", "role_id", "scope", "mode"}}
		if strings.Contains(q, "holder_kind =") {
			if len(args) != 2 || args[0].Value != "account" || args[1].Value != c.target {
				return nil, errors.New("held-by grant lookup lost target")
			}
			if c.target == 11 {
				return nil, errors.New("private held-by detail")
			}
			if c.target == 9 || c.target == 14 || c.target == 13 {
				r.rows = [][]driver.Value{{int64(90), "account", c.target, int64(1), "*", "active"}}
			}
		} else {
			if len(args) != 1 || args[0].Value != "mailbox:owner@example.org" || !strings.Contains(q, "scope =") {
				return nil, errors.New("held-on lookup lost mailbox scope")
			}
			if c.target == 12 {
				return nil, errors.New("private held-on detail")
			}
		}
		return r, nil
	}
	if strings.Contains(q, `FROM "roles"`) {
		c.record(q, args)
		return &authReadSnapshotRows{columns: []string{"id", "name"}, rows: [][]driver.Value{{int64(1), "superadmin"}}}, nil
	}
	if strings.Contains(q, "activation_expires_at") && strings.Contains(q, "JOIN role_capabilities") {
		c.record(q, args)
		cap := "*"
		if c.target == 14 {
			cap = "mailboxes:delete"
		}
		return &authReadSnapshotRows{columns: []string{"scope", "capability", "expires_at", "activation_expires_at"}, rows: [][]driver.Value{{"*", cap, nil, nil}}}, nil
	}
	if strings.Contains(q, "FROM role_capabilities rc") {
		c.record(q, args)
		return &authReadSnapshotRows{columns: []string{"name"}, rows: [][]driver.Value{{"*"}}}, nil
	}
	if strings.Contains(q, `FROM "grant_activations"`) {
		c.record(q, args)
		return &authReadSnapshotRows{columns: []string{"id"}}, nil
	}
	if strings.Contains(q, "count(*)") && strings.Contains(q, "grants AS g") {
		c.record(q, args)
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func (c *mailboxRemovalConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "SELECT pg_advisory_xact_lock(") {
		if len(args) == 1 && args[0].Value == int64(0x6772616e74636867) {
			if c.tx == nil {
				return nil, errors.New("grant lock outside transaction")
			}
			c.tx.relevant = true
			c.record("BEGIN", nil)
			c.record(q, args)
			if c.target == 4 {
				return nil, errors.New("private grant lock detail")
			}
		}
		return driver.RowsAffected(0), nil
	}
	if strings.HasPrefix(q, `UPDATE "refresh_tokens"`) {
		c.record(q, args)
		if len(args) != 4 || args[2].Value != c.target || args[3].Value != "active" || !strings.Contains(q, "account_id =") || !strings.Contains(q, "status =") {
			return nil, errors.New("refresh revocation lost target/active selector")
		}
		stamp, ok := args[1].Value.(time.Time)
		if args[0].Value != "revoked" || !ok || stamp.Before(time.Now().Add(-time.Minute)) || stamp.After(time.Now()) {
			return nil, errors.New("refresh revocation lost status/request time")
		}
		if c.target == 6 {
			return nil, errors.New("private refresh detail")
		}
		return driver.RowsAffected(2), nil
	}
	if strings.HasPrefix(q, `DELETE FROM "grant_activations"`) || strings.HasPrefix(q, `DELETE FROM "grants"`) {
		c.record(q, args)
		return driver.RowsAffected(1), nil
	}
	if q == `DELETE FROM "mailboxes" WHERE "mailboxes"."id" = $1` && len(args) == 1 && args[0].Value == c.target {
		c.record(q, args)
		if c.target == 7 {
			return nil, errors.New("private mailbox deletion detail")
		}
		if c.tx == nil {
			return nil, errors.New("mailbox deletion outside transaction")
		}
		c.tx.removed = true
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("unexpected ancillary mailbox write")
}

func newMailboxRemovalScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	token := freshMailboxToken(t, jwt)
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":        {Capability: middleware.CapMailboxesDel, Scope: "*"},
		"key_read":         {Capability: middleware.CapMailboxesRead, Scope: "*"},
		"key_domain":       {Capability: middleware.CapMailboxesDel, Scope: "domain:example.org"},
		"key_mailbox":      {Capability: middleware.CapMailboxesDel, Scope: "mailbox:owner@example.org"},
		"key_wrong_domain": {Capability: middleware.CapMailboxesDel, Scope: "domain:other.test"},
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
					return grants.Held{}, errors.New("private session grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &mailboxRemovalConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store, deleted: map[int64]bool{}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		var cancel context.CancelFunc
		if err := db.Callback().Query().After("gorm:after_query").Register("example:cancel-mailbox-removal", func(tx *gorm.DB) {
			if cancel != nil && strings.HasPrefix(tx.Statement.SQL.String(), `SELECT * FROM "mailboxes"`) {
				cancel()
			}
		}); err != nil {
			t.Fatal(err)
		}
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get("X-Example-Cancel") == "after-mailbox-lookup" {
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
		"body_boundary",
		"cancelled_after_lookup",
		"cookie_session",
		"credentials_unavailable",
		"csrf_mismatch",
		"csrf_missing",
		"delete_unavailable",
		"deleted",
		"domain_scope",
		"grant_exceeds_held",
		"held_by_unavailable",
		"held_on_unavailable",
		"inactive",
		"invalid_format",
		"invalid_id",
		"key_domain",
		"key_holder_missing",
		"key_mailbox",
		"key_read",
		"key_write",
		"key_wrong_domain",
		"last_superadmin",
		"lock_unavailable",
		"lookup_unavailable",
		"mailbox_scope",
		"missing",
		"permission_store_unavailable",
		"rate_limited",
		"read_only",
		"refresh_unavailable",
		"request_too_large",
		"scope_lookup_unavailable",
		"scoped_missing",
		"session_unavailable",
		"two_factor_required",
		"unauthenticated",
		"write_only",
		"wrong_domain",
	} {
		out[name] = normal
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesDel, Scope: grants.All}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesDel, Scope: grants.Domain("example.org")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesDel, Scope: grants.Mailbox("owner@example.org")}}, false, false)
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
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "mailbox-session-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "mailbox-session-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong")
			}
			normal.handler.ServeHTTP(w, r)
		}), ""}, normal.store}
	}
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesDel, Scope: grants.Domain("other.test")}}, false, false)
	out["scope_lookup_unavailable"] = out["domain_scope"]
	out["scoped_missing"] = out["domain_scope"]
	self := makeRouter(wild, false, false)
	out["self_delete"] = self
	key := self
	key.token = tokens["key_write"]
	out["self_key"] = key
	plain := normal
	plain.token = mailboxToken(t, jwt)
	out["two_factor_required"] = plain
	mfa, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"last_superadmin", "grant_exceeds_held"} {
		s := normal
		s.token = mfa.AccessToken
		out[name] = s
	}
	out["cancelled_after_lookup"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Example-Cancel", "after-mailbox-lookup")
		normal.handler.ServeHTTP(w, r)
	}), token}, normal.store}
	return out
}

func TestMailboxRemovalLookupAndCredentialKinds(t *testing.T) {
	t.Run("lookup failure", func(t *testing.T) {
		s := newMailboxRemovalScenarios(t)["write_only"]
		rr := mailboxSessionEndingRequest(t, s, http.MethodDelete, "/api/v1/admin/mailboxes/3", nil)
		if rr.Code != 500 {
			t.Fatalf("lookup failure:%d %s want500", rr.Code, rr.Body)
		}
	})
	t.Run("all authenticators", func(t *testing.T) {
		s := newMailboxRemovalScenarios(t)["write_only"]
		rr := mailboxSessionEndingRequest(t, s, http.MethodDelete, "/api/v1/admin/mailboxes/1", nil)
		if rr.Code != 204 {
			t.Fatalf("delete:%d %s", rr.Code, rr.Body)
		}
		for _, q := range s.store.snapshot() {
			if strings.HasPrefix(q.query, `UPDATE "credentials"`) {
				want := map[string]bool{"password": false, "totp": false, "recovery_code": false, "app_password": false, "api_key": false}
				for _, arg := range q.args {
					if kind, ok := arg.Value.(string); ok {
						if _, ok := want[kind]; ok {
							want[kind] = true
						}
					}
				}
				for kind, present := range want {
					if !present {
						t.Errorf("deleted account left %s authenticator unrevoked", kind)
					}
				}
				return
			}
		}
		t.Fatal("no credential revocation")
	})
}

func TestMailboxRemovalInvalidatesCachedSession(t *testing.T) {
	f := newMailboxRemovalScenarios(t)
	s := f["self_delete"]
	rr := mailboxSessionEndingRequest(t, s, http.MethodGet, "/api/v1/accounts/10/credentials", nil)
	if rr.Code != 200 {
		t.Fatalf("prime:%d %s", rr.Code, rr.Body)
	}
	rr = mailboxSessionEndingRequest(t, s, http.MethodDelete, "/api/v1/admin/mailboxes/10", nil)
	if rr.Code != 204 {
		t.Fatalf("self deletion:%d %s", rr.Code, rr.Body)
	}
	rr = mailboxSessionEndingRequest(t, s, http.MethodGet, "/api/v1/accounts/10/credentials", nil)
	if rr.Code != 401 {
		t.Fatalf("cached deleted session:%d %s", rr.Code, rr.Body)
	}
	rr = mailboxSessionEndingRequest(t, f["self_key"], http.MethodDelete, "/api/v1/admin/mailboxes/1", nil)
	if rr.Code != 401 {
		t.Fatalf("deleted account API key:%d %s", rr.Code, rr.Body)
	}
}
