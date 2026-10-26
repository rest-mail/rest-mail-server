package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

const serviceProbePath = "/api/v1/admin/test/probe"

type serviceProbeContextKey struct{}

type serviceProbeReadHook struct {
	io.ReadCloser
	before func()
}

func (b *serviceProbeReadHook) Read(p []byte) (int, error) {
	if b.before != nil {
		b.before()
		b.before = nil
	}
	return b.ReadCloser.Read(p)
}

type serviceProbeConnection struct {
	domainMutationConnection
	mode      string
	cancel    context.CancelFunc
	pings     int
	contextOK bool
}

func (c *serviceProbeConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *serviceProbeConnection) Driver() driver.Driver { return c }

func (c *serviceProbeConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *serviceProbeConnection) Ping(ctx context.Context) error {
	c.pings++
	c.contextOK = ctx.Value(serviceProbeContextKey{}) == "HTTP probe"
	_, hasDeadline := ctx.Deadline()
	c.record("PING", []driver.NamedValue{{Name: "http_context", Value: c.contextOK}, {Name: "deadline", Value: hasDeadline}})
	if c.cancel != nil {
		c.cancel()
	}
	if c.mode == "db_failure" {
		return errors.New("fixture database unavailable")
	}
	return ctx.Err()
}

func newServiceProbeScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_write":        {Capability: grants.Wildcard, Scope: "*"},
		"key_read":         {Capability: middleware.CapDomainsRead, Scope: "*"},
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
	makeRouter := func(entries []grants.Entry, fail, limited bool, mode string) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if fail {
					return grants.Held{}, errors.New("private domain grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &serviceProbeConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}, mode: mode}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)

		environment := "test"
		if mode == "production" {
			environment = "production"
		}
		r := NewRouters(db, jwt, &config.Config{Environment: environment, APIMaxBodyBytes: 512, AuthRateLimitEnabled: limited, AuthRateLimitRPS: 1.0 / 3600, AuthRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		if mode == "production" {
			doc := contractDocument(t, r.generator("production probe boundary"))
			if _, ok := doc["paths"].(map[string]interface{})[serviceProbePath]; ok {
				t.Fatal("production describes service probe")
			}
		}
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.reads = 0
			c.current = nil
			c.createdName = ""
			if req.Header.Get("X-Example-Cancel") == "cancel-probe" {
				var ctx context.Context
				ctx, c.cancel = context.WithCancel(req.Context())
				defer func() { c.cancel(); c.cancel = nil }()
				req = req.WithContext(ctx)
			}
			c.pings = 0
			c.contextOK = false
			req = req.WithContext(context.WithValue(req.Context(), serviceProbeContextKey{}, "HTTP probe"))
			if mode == "setup_failure" {
				oldPool, oldStatement := db.ConnPool, db.Statement.ConnPool
				req.Body = &serviceProbeReadHook{ReadCloser: req.Body, before: func() { db.ConnPool = nil; db.Statement.ConnPool = nil }}
				defer func() { db.ConnPool = oldPool; db.Statement.ConnPool = oldStatement }()
			}
			r.Public.ServeHTTP(w, req)
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false, "normal")
	out := map[string]bulkQueueExampleScenario{}
	// Explicit fixture cases keep regression tests independent of published examples.
	for _, name := range []string{
		"body_array",
		"body_boundary",
		"cancelled_probe",
		"cookie_session",
		"csrf_mismatch",
		"csrf_missing",
		"database_unavailable",
		"domain_scope",
		"duplicate",
		"empty_service",
		"explicit_timeout",
		"host_unused",
		"invalid_timeout",
		"key_domain",
		"key_holder_missing",
		"key_mailbox",
		"key_read",
		"key_write",
		"key_wrong_domain",
		"mailbox_scope",
		"malformed_json",
		"mixed_unknown",
		"null_body",
		"permission_store_unavailable",
		"probed",
		"rate_limited",
		"read_only",
		"request_too_large",
		"service_unknown",
		"session_unavailable",
		"setup_failure",
		"unauthenticated",
		"uppercase",
		"write_only",
		"wrong_domain",
	} {
		out[name] = normal
	}
	out["write_only"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}, false, false, "normal")
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.All}}, false, false, "normal")
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Domain("example.com")}}, false, false, "normal")
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Mailbox("owner@example.com")}}, false, false, "normal")
	out["permission_store_unavailable"] = makeRouter(nil, true, false, "normal")
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
	limited := makeRouter(wild, false, true, "normal")
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
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.Domain("other.test")}}, false, false, "normal")

	out["cancelled_probe"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Example-Cancel", "cancel-probe")
		normal.handler.ServeHTTP(w, r)
	}), token}, normal.store}
	out["database_unavailable"] = makeRouter(wild, false, false, "db_failure")
	out["setup_failure"] = makeRouter(wild, false, false, "setup_failure")
	out["production"] = makeRouter(wild, false, false, "production")
	return out
}

func TestServiceProbeCancellation(t *testing.T) {
	s := newServiceProbeScenarios(t)["cancelled_probe"]
	rr := mailboxSessionEndingRequest(t, s, http.MethodPost, serviceProbePath, []byte(`{"services":["db"]}`))
	if rr.Code != 200 {
		t.Fatalf("status%d", rr.Code)
	}
	var body struct {
		Data struct {
			DB struct{ Status, Error string }
		}
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.DB.Status != "error" || body.Data.DB.Error != "context canceled" {
		t.Fatal("database probe ignored HTTP cancellation")
	}
}
