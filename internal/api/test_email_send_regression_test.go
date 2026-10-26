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

const testEmailSendPath = "/api/v1/admin/test/send"

type testEmailSendConnection struct {
	testDataMutationConnection
	writes           int
	cancelAfterInbox context.CancelFunc
}

func (c *testEmailSendConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *testEmailSendConnection) Driver() driver.Driver { return c }

func (c *testEmailSendConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *testEmailSendConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `SELECT * FROM "mailboxes"`) && strings.Contains(q, "address =") {
		c.relevant(ctx, q, args)
		if len(args) < 1 {
			return nil, errors.New("sender lookup lost address")
		}
		address, ok := args[0].Value.(string)
		if !ok {
			return nil, errors.New("mailbox lookup address type changed")
		}
		sender := address == "postmaster@restmail.test"
		if (sender && c.mode == "sender_lookup_failure") || (!sender && c.mode == "recipient_lookup_failure") {
			return nil, errors.New("private mailbox lookup detail")
		}
		rows := &authReadSnapshotRows{columns: []string{"id", "address", "display_name"}}
		if (sender && c.mode == "sender_missing") || (!sender && c.mode == "recipient_missing") {
			return rows, nil
		}
		id := int64(42)
		if sender {
			id = 41
		}
		rows.rows = [][]driver.Value{{id, address, "Fixture Postmaster"}}
		return rows, nil
	}
	if strings.HasPrefix(q, `INSERT INTO "messages"`) {
		c.relevant(ctx, q, args)
		c.writes++
		if (c.writes == 1 && c.mode == "inbox_failure") || (c.writes == 2 && c.mode == "sent_failure") {
			return nil, errors.New("private message insertion detail")
		}
		if c.writes == 1 && c.cancelAfterInbox != nil {
			c.cancelAfterInbox()
			c.cancelAfterInbox = nil
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(c.writes)}}}, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func newTestEmailSendScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
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
		c := &testEmailSendConnection{testDataMutationConnection: testDataMutationConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }

		environment := "test"
		if production {
			environment = "production"
		}
		r := NewRouters(db, jwt, &config.Config{Environment: environment, APIMaxBodyBytes: 512}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.mode = req.Header.Get("X-Fixture-Data-Mode")
			c.nextID = 100
			c.writes = 0
			c.cancelAfterInbox = nil
			c.tx = nil
			c.aborted = false
			c.cleared = false
			ctx, cancel := context.WithCancel(context.WithValue(req.Context(), testDataMutationContextKey{}, true))
			defer cancel()
			c.cancel = nil
			if c.mode == "cancel_after_inbox" {
				c.cancelAfterInbox = cancel
			}
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
		"cancel_after_inbox",
		"cancelled",
		"commit_failure",
		"cookie_session",
		"created",
		"csrf_mismatch",
		"csrf_missing",
		"default_body",
		"default_subject",
		"defaults",
		"domain_scope",
		"from_empty",
		"from_missing",
		"inbox_failure",
		"key_admin",
		"key_domain",
		"key_domain_read",
		"key_holder_missing",
		"key_mailbox",
		"key_wrong_domain",
		"mailbox_scope",
		"malformed_json",
		"non_admin",
		"non_object",
		"permission_store_unavailable",
		"recipient_lookup_failure",
		"recipient_missing",
		"request_too_large",
		"self_send",
		"sender_lookup_failure",
		"sender_missing",
		"sent_failure",
		"session_unavailable",
		"to_empty",
		"to_missing",
		"unauthenticated",
		"unknown_field",
		"utf8",
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

	for _, name := range []string{"sender_missing", "recipient_missing", "sender_lookup_failure", "recipient_lookup_failure", "inbox_failure", "sent_failure", "commit_failure", "cancelled", "cancel_after_inbox"} {
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

func TestTestEmailSendProductionAbsent(t *testing.T) {
	s := newTestEmailSendScenarios(t)["production"]
	rr := mailboxSessionEndingRequest(t, s, http.MethodPost, testEmailSendPath, []byte(`{}`))
	if rr.Code != 404 || len(s.store.snapshot()) != 0 {
		t.Fatal("production exposed test injection")
	}
}
