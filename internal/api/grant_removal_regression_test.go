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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

// Controlled SQL exercises real grant policy and audit wiring. PostgreSQL
// regressions separately exercise durability, rollback and cache invalidation.
type grantRemovalConnection struct {
	credentialRevocationConnection
	target        int64
	tx            *grantRemovalTx
	self, revoked bool
	seq           int64
	hash          string
}

type grantRemovalTx struct {
	c                    *grantRemovalConnection
	relevant, removeSelf bool
	seq                  int64
	hash                 string
}

func (c *grantRemovalConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *grantRemovalConnection) Driver() driver.Driver { return c }

func (c *grantRemovalConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *grantRemovalConnection) Begin() (driver.Tx, error) {
	tx := &grantRemovalTx{c: c, seq: c.seq, hash: c.hash}
	c.tx = tx
	return tx, nil
}

func (tx *grantRemovalTx) Commit() error {
	if tx.relevant {
		tx.c.record("COMMIT", nil)
		tx.c.seq = tx.seq
		tx.c.hash = tx.hash
		if tx.removeSelf {
			tx.c.revoked = true
		}
	}
	tx.c.tx = nil
	return nil
}

func (tx *grantRemovalTx) Rollback() error {
	if tx.relevant {
		tx.c.record("ROLLBACK", nil)
	}
	tx.c.tx = nil
	return nil
}

func (c *grantRemovalConnection) auditRow(q string, args []driver.NamedValue) (driver.Rows, error) {
	c.record(q, args)
	fields, err := revocationAuditFields(q, args)
	if err != nil {
		return nil, err
	}
	action := fields["action"]
	if c.target == 13 || c.target == 19 && action == grants.ActionDeactivate {
		return nil, errors.New("private grant audit detail")
	}
	var target map[string]string
	switch raw := fields["target"].(type) {
	case string:
		err = json.Unmarshal([]byte(raw), &target)
	case []byte:
		err = json.Unmarshal(raw, &target)
	default:
		err = errors.New("audit target missing")
	}
	if err != nil {
		return nil, err
	}
	if target["grant_id"] != strconv.FormatInt(c.target, 10) || fields["actor_id"] != int64(10) || fields["outcome"] != "success" {
		return nil, errors.New("grant audit lost actor/target/outcome")
	}
	if c.tx == nil || !c.tx.relevant || fields["seq"] != c.tx.seq+1 {
		return nil, errors.New("grant audit escaped transaction/sequence")
	}
	c.tx.seq++
	c.tx.hash = fmt.Sprint(fields["hash"])
	return &authReadSnapshotRows{columns: []string{"seq"}, rows: [][]driver.Value{{c.tx.seq}}}, nil
}

func (c *grantRemovalConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `SELECT * FROM "grants"`) {
		c.record(q, args)
		if len(args) != 2 || args[0].Value != c.target || args[1].Value != int64(1) || !strings.Contains(q, `"grants"."id" =`) {
			return nil, errors.New("grant lookup lost exact ID/bound")
		}
		if c.target == 3 {
			return nil, errors.New("private grant lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "holder_kind", "holder_id", "role_id", "scope", "mode", "expires_at"}}
		if c.target == 2147483647 {
			return r, nil
		}
		role, holder, scope, mode := int64(1), int64(20), "mailbox:owner@example.org", "active"
		var expires driver.Value
		if c.target == 9 || c.target == 17 || c.target == 18 {
			role = 2
			scope = "*"
		}
		if c.target == 15 || c.target == 16 || c.target == 19 {
			mode = "eligible"
		}
		if c.target == 17 {
			expires = time.Now().Add(-time.Hour)
		}
		if c.target == 23 {
			scope = "domain:example.org"
		}
		if c.self && c.target == 22 {
			role = 3
			holder = 10
			scope = "*"
		}
		r.rows = [][]driver.Value{{c.target, "account", holder, role, scope, mode, expires}}
		return r, nil
	}
	if strings.Contains(q, `FROM "roles"`) {
		c.record(q, args)
		if c.target == 11 {
			return nil, errors.New("private role load detail")
		}
		return &authReadSnapshotRows{columns: []string{"id", "name"}, rows: [][]driver.Value{{args[0].Value, "Public test role"}}}, nil
	}
	if strings.Contains(q, "activation_expires_at") && strings.Contains(q, "JOIN role_capabilities") {
		c.record(q, args)
		if c.target == 5 {
			return nil, errors.New("private grant authorizer detail")
		}
		r := &authReadSnapshotRows{columns: []string{"scope", "capability", "expires_at", "activation_expires_at"}}
		if !c.self || !c.revoked {
			cap := "*"
			if c.target == 10 || c.self {
				cap = "users:write"
			}
			r.rows = [][]driver.Value{{"*", cap, nil, nil}}
		}
		return r, nil
	}
	if strings.Contains(q, "FROM role_capabilities rc") {
		c.record(q, args)
		cap := "mail:read"
		if args[0].Value == int64(2) {
			cap = "*"
		}
		if args[0].Value == int64(3) {
			cap = "users:write"
		}
		return &authReadSnapshotRows{columns: []string{"name"}, rows: [][]driver.Value{{cap}}}, nil
	}
	if strings.HasPrefix(q, `SELECT "active" FROM "mailboxes"`) {
		c.record(q, args)
		return &authReadSnapshotRows{columns: []string{"active"}, rows: [][]driver.Value{{c.target != 18}}}, nil
	}
	if strings.Contains(q, `FROM "grant_activations"`) {
		c.record(q, args)
		if len(args) != 3 || args[0].Value != c.target || args[2].Value != int64(1) || !strings.Contains(q, "ended_at IS NULL AND expires_at >") {
			return nil, errors.New("activation lookup lost grant/live/bound selectors")
		}
		if c.target == 6 {
			return nil, errors.New("private activation lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "grant_id", "activated_at", "expires_at", "reason"}}
		if c.target == 15 || c.target == 19 {
			r.rows = [][]driver.Value{{int64(51), c.target, time.Now().Add(-time.Minute), time.Now().Add(time.Hour), "Public test activation"}}
		}
		return r, nil
	}
	if strings.Contains(q, "count(*)") && strings.Contains(q, "grants AS g") {
		c.record(q, args)
		if c.target == 12 {
			return nil, errors.New("private remaining admin detail")
		}
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}, nil
	}
	if c.tx != nil && c.tx.relevant {
		if strings.HasPrefix(q, "SELECT seq, hash FROM audit_log") {
			c.record(q, args)
			if c.target == 14 {
				return nil, errors.New("private audit chain read detail")
			}
			r := &authReadSnapshotRows{columns: []string{"seq", "hash"}}
			if c.tx.seq > 0 {
				r.rows = [][]driver.Value{{c.tx.seq, c.tx.hash}}
			}
			return r, nil
		}
		if strings.HasPrefix(q, `INSERT INTO "audit_log"`) {
			return c.auditRow(q, args)
		}
	}
	return c.credentialRevocationConnection.QueryContext(ctx, q, args)
}

func (c *grantRemovalConnection) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "SELECT pg_advisory_xact_lock(") && len(args) == 1 && args[0].Value == int64(0x6772616e74636867) {
		if c.tx == nil {
			return nil, errors.New("grant lock outside transaction")
		}
		c.tx.relevant = true
		c.record("BEGIN", nil)
		c.record(q, args)
		if c.target == 4 {
			return nil, errors.New("private grant lock detail")
		}
		return driver.RowsAffected(0), nil
	}
	if strings.HasPrefix(q, `DELETE FROM "grant_activations"`) || strings.HasPrefix(q, `DELETE FROM "grants"`) {
		c.record(q, args)
		if len(args) != 1 || args[0].Value != c.target {
			return nil, errors.New("grant removal lost identity")
		}
		if c.target == 7 && strings.HasPrefix(q, `DELETE FROM "grant_activations"`) || c.target == 8 && strings.HasPrefix(q, `DELETE FROM "grants"`) {
			return nil, errors.New("private grant deletion detail")
		}
		if c.self && c.target == 22 && strings.HasPrefix(q, `DELETE FROM "grants"`) {
			c.tx.removeSelf = true
		}
		return driver.RowsAffected(1), nil
	}
	if c.tx != nil && c.tx.relevant && strings.HasPrefix(q, `INSERT INTO "audit_log"`) {
		_, err := c.auditRow(q, args)
		return driver.RowsAffected(1), err
	}
	return c.credentialRevocationConnection.ExecContext(ctx, q, args)
}

func newGrantRemovalScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	passwordToken := mailboxToken(t, jwt)
	session, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	token := session.AccessToken
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":        {Capability: middleware.CapUsersWrite, Scope: "*"},
		"key_read":         {Capability: middleware.CapUsersRead, Scope: "*"},
		"key_domain":       {Capability: middleware.CapUsersWrite, Scope: "domain:example.org"},
		"key_mailbox":      {Capability: middleware.CapUsersWrite, Scope: "mailbox:owner@example.org"},
		"key_wrong_domain": {Capability: middleware.CapUsersWrite, Scope: "domain:other.test"},
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
	makeRouter := func(entries []grants.Entry, fail, limited bool, real ...bool) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if fail {
					return grants.Held{}, errors.New("private session grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		self := len(real) > 0 && real[0]
		if self {
			heldGrants = cachedHeldGrants
		}
		c := &grantRemovalConnection{credentialRevocationConnection: credentialRevocationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}, self: self}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method == http.MethodDelete {
				c.target, _ = strconv.ParseInt(req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:], 10, 64)
			}
			r.Public.ServeHTTP(w, req)
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	// Explicit fixture cases keep regression tests independent of published examples.
	for _, name := range []string{
		"activation_audit_unavailable",
		"activation_delete_unavailable",
		"activation_lookup_unavailable",
		"audit_read_unavailable",
		"audit_unavailable",
		"authorize_unavailable",
		"body_boundary",
		"cancelled",
		"cookie_session",
		"csrf_mismatch",
		"csrf_missing",
		"deleted",
		"domain_grant",
		"domain_scope",
		"eligible",
		"expired_superadmin",
		"grant_delete_unavailable",
		"grant_exceeds_held",
		"inactive_superadmin",
		"invalid_format",
		"invalid_id",
		"key_domain",
		"key_mailbox",
		"key_read",
		"key_write",
		"key_wrong_domain",
		"last_superadmin",
		"live_activation",
		"lock_unavailable",
		"lookup_unavailable",
		"mailbox_scope",
		"missing",
		"permission_store_unavailable",
		"rate_limited",
		"read_only",
		"remaining_unavailable",
		"request_too_large",
		"role_unavailable",
		"session_unavailable",
		"two_factor_required",
		"unauthenticated",
		"write_only",
		"wrong_domain",
	} {
		out[name] = normal
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapUsersWrite, Scope: grants.All}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapUsersRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapUsersWrite, Scope: grants.Domain("example.org")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapUsersWrite, Scope: grants.Mailbox("owner@example.org")}}, false, false)
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
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: middleware.CapUsersWrite, Scope: grants.Domain("other.test")}}, false, false)
	out["scope_lookup_unavailable"] = out["domain_scope"]
	out["scoped_missing"] = out["domain_scope"]
	self := makeRouter(wild, false, false, true)
	out["self_delete"] = self
	key := self
	key.token = tokens["key_write"]
	out["self_key"] = key
	plain := normal
	plain.token = passwordToken
	out["two_factor_required"] = plain
	out["cancelled"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		normal.handler.ServeHTTP(w, r.WithContext(ctx))
	}), token}, normal.store}
	return out
}

func TestGrantRemovalHonorsCancellation(t *testing.T) {
	s := newGrantRemovalScenarios(t)["cancelled"]
	rr := mailboxSessionEndingRequest(t, s, http.MethodDelete, "/api/v1/admin/grants/1", nil)
	if rr.Code != 500 {
		t.Fatalf("cancelled grant removal:%d %s want500", rr.Code, rr.Body)
	}
	for _, q := range s.store.snapshot() {
		if strings.HasPrefix(q.query, `DELETE FROM "grants"`) {
			t.Fatal("cancelled request removed a grant")
		}
	}
}

func TestGrantRemovalInvalidatesPermissionCache(t *testing.T) {
	s := newGrantRemovalScenarios(t)["self_delete"]
	rr := mailboxSessionEndingRequest(t, s, http.MethodDelete, "/api/v1/admin/grants/2147483647", nil)
	if rr.Code != 404 {
		t.Fatalf("prime grant cache:%d %s", rr.Code, rr.Body)
	}
	rr = mailboxSessionEndingRequest(t, s, http.MethodDelete, "/api/v1/admin/grants/22", nil)
	if rr.Code != 204 {
		t.Fatalf("remove own permission:%d %s", rr.Code, rr.Body)
	}
	rr = mailboxSessionEndingRequest(t, s, http.MethodDelete, "/api/v1/admin/grants/1", nil)
	if rr.Code != 403 {
		t.Fatalf("cached removed permission:%d %s", rr.Code, rr.Body)
	}
}
