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
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

// Controlled SQL proves revocation selectors, transaction intent and session
// cache wiring. A scratch-database regression checks actual rollback/durability.
type mailboxSessionEndingConnection struct {
	accountCredentialListConnection
	store  *bulkQueueExampleStore
	tx     *mailboxSessionEndingTx
	epochs map[int64]int
}

func (c *mailboxSessionEndingConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *mailboxSessionEndingConnection) Driver() driver.Driver { return c }

func (c *mailboxSessionEndingConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *mailboxSessionEndingConnection) record(q string, args []driver.NamedValue) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
}

type mailboxSessionEndingTx struct {
	c        *mailboxSessionEndingConnection
	relevant bool
	pending  map[int64]int
}

func (c *mailboxSessionEndingConnection) Begin() (driver.Tx, error) {
	tx := &mailboxSessionEndingTx{c: c, pending: map[int64]int{}}
	c.tx = tx
	return tx, nil
}

func (tx *mailboxSessionEndingTx) Commit() error {
	if tx.relevant {
		if tx.c.epochs == nil {
			tx.c.epochs = map[int64]int{}
		}
		for id, n := range tx.pending {
			tx.c.epochs[id] = n
		}
		tx.c.record("COMMIT", nil)
	}
	tx.c.tx = nil
	return nil
}

func (tx *mailboxSessionEndingTx) Rollback() error {
	if tx.relevant {
		tx.c.record("ROLLBACK", nil)
	}
	tx.c.tx = nil
	return nil
}

func (c *mailboxSessionEndingConnection) mutation(q string, args []driver.NamedValue) {
	if c.tx != nil && !c.tx.relevant {
		c.record("BEGIN", nil)
		c.tx.relevant = true
	}
	c.record(q, args)
}

func (c *mailboxSessionEndingConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, `FROM "mailboxes"`) {
		if strings.HasPrefix(q, `SELECT "address"`) && !strings.Contains(q, "active") {
			c.record(q, args)
			if len(args) != 1 || !strings.Contains(q, "id =") {
				return nil, errors.New("mailbox scope lost ID")
			}
			if args[0].Value == int64(8) {
				return nil, errors.New("private session scope detail")
			}
			r := &authReadSnapshotRows{columns: []string{"address"}}
			if args[0].Value != int64(2147483647) {
				r.rows = [][]driver.Value{{"owner@example.org"}}
			}
			return r, nil
		}
		if strings.HasPrefix(q, `SELECT "id" FROM`) {
			c.record(q, args)
			if len(args) != 2 || !strings.Contains(q, `"mailboxes"."id" =`) || args[1].Value != int64(1) {
				return nil, errors.New("session target lost exact ID/bound")
			}
			if args[0].Value == int64(3) {
				return nil, errors.New("private session target lookup detail")
			}
			r := &authReadSnapshotRows{columns: []string{"id"}}
			if args[0].Value != int64(2147483647) {
				r.rows = [][]driver.Value{{args[0].Value}}
			}
			return r, nil
		}
		if strings.Contains(q, `"credential_epoch"`) {
			if len(args) < 1 {
				return nil, errors.New("session cache read lost account")
			}
			id := args[0].Value.(int64)
			return &authReadSnapshotRows{columns: []string{"id", "active", "credential_epoch", "password_change_required", "bootstrap_expires_at"}, rows: [][]driver.Value{{id, true, int64(c.epochs[id]), false, nil}}}, nil
		}
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func (c *mailboxSessionEndingConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, `UPDATE "mailboxes" SET`) {
		c.mutation(q, args)
		if len(args) != 2 || !strings.Contains(q, `"credential_epoch"=credential_epoch + 1`) || !strings.Contains(q, "WHERE id =") {
			return nil, errors.New("session ending lost atomic epoch increment or target")
		}
		id := args[1].Value.(int64)
		if id == 4 {
			return nil, errors.New("private epoch update detail")
		}
		if c.tx == nil {
			return nil, errors.New("epoch changed outside transaction")
		}
		c.tx.pending[id] = c.epochs[id] + 1
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, `UPDATE "refresh_tokens" SET`) {
		c.mutation(q, args)
		if len(args) != 4 || !strings.Contains(q, "WHERE account_id =") || !strings.Contains(q, "AND status =") || args[0].Value != models.RefreshTokenRevoked || args[3].Value != models.RefreshTokenActive {
			return nil, errors.New("revocation lost account/active selector or revoked state")
		}
		id := args[2].Value.(int64)
		if id == 5 {
			return nil, errors.New("private refresh revocation detail")
		}
		if id == 6 {
			return driver.RowsAffected(0), nil
		}
		return driver.RowsAffected(2), nil
	}
	return nil, errors.New("unexpected password/credential/resource mutation")
}

func newMailboxSessionEndingScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	token := mailboxToken(t, jwt)
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":        {Capability: middleware.CapMailboxesWrite, Scope: "*"},
		"key_read":         {Capability: middleware.CapMailboxesRead, Scope: "*"},
		"key_domain":       {Capability: middleware.CapMailboxesWrite, Scope: "domain:example.org"},
		"key_mailbox":      {Capability: middleware.CapMailboxesWrite, Scope: "mailbox:owner@example.org"},
		"key_wrong_domain": {Capability: middleware.CapMailboxesWrite, Scope: "domain:other.test"},
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
		c := &mailboxSessionEndingConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return sieveInstallationTime }
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{r.Public, token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	for _, name := range []string{"ended", "inactive", "self_end", "no_tokens", "missing", "lookup_unavailable", "epoch_unavailable", "refresh_unavailable", "invalid_id", "invalid_format", "request_too_large"} {
		out[name] = normal
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesWrite, Scope: grants.All}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesRead, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesWrite, Scope: grants.Domain("example.org")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesWrite, Scope: grants.Mailbox("owner@example.org")}}, false, false)
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
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: middleware.CapMailboxesWrite, Scope: grants.Domain("other.test")}}, false, false)
	out["scope_lookup_unavailable"] = out["domain_scope"]
	out["scoped_missing"] = out["domain_scope"]
	self := makeRouter(wild, false, false)
	out["self_end"] = self
	key := self
	key.token = tokens["key_write"]
	out["self_key"] = key
	return out
}

func mailboxSessionEndingRequest(t *testing.T, s bulkQueueExampleScenario, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if s.token != "" {
		r.Header.Set("Authorization", "Bearer "+s.token)
	}
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, r)
	return rr
}

func TestMailboxSessionEndingLookupFailureIsNotMissing(t *testing.T) {
	s := newMailboxSessionEndingScenarios(t)["ended"]
	rr := mailboxSessionEndingRequest(t, s, http.MethodDelete, "/api/v1/admin/mailboxes/3/sessions", nil)
	if rr.Code != 500 {
		t.Fatalf("lookup failure got%d %s want500", rr.Code, rr.Body)
	}
}

func TestMailboxSessionEndingInvalidatesCachedSession(t *testing.T) {
	f := newMailboxSessionEndingScenarios(t)
	s := f["self_end"]
	rr := mailboxSessionEndingRequest(t, s, http.MethodGet, "/api/v1/accounts/10/credentials", nil)
	if rr.Code != 200 {
		t.Fatalf("prime session:%d %s", rr.Code, rr.Body)
	}
	rr = mailboxSessionEndingRequest(t, s, http.MethodDelete, "/api/v1/admin/mailboxes/10/sessions", nil)
	if rr.Code != 204 {
		t.Fatalf("end self:%d %s", rr.Code, rr.Body)
	}
	rr = mailboxSessionEndingRequest(t, s, http.MethodGet, "/api/v1/accounts/10/credentials", nil)
	if rr.Code != 401 {
		t.Fatalf("cached old epoch still accepted:%d %s", rr.Code, rr.Body)
	}
	rr = mailboxSessionEndingRequest(t, f["self_key"], http.MethodDelete, "/api/v1/admin/mailboxes/1/sessions", nil)
	if rr.Code != 204 {
		t.Fatalf("API key was incorrectly ended with sessions:%d %s", rr.Code, rr.Body)
	}
}
