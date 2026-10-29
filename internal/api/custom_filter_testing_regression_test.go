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

type customFilterTestingContextKey struct{}

type customFilterTestingConnection struct{ domainMutationConnection }

func (c *customFilterTestingConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *customFilterTestingConnection) Driver() driver.Driver { return c }

func (c *customFilterTestingConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *customFilterTestingConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if rows, ok, err := pipelineDomainRows(q, args); ok {
		return rows, err
	}
	if strings.Contains(q, `FROM "custom_filters"`) {
		c.record(q, args)
		c.record("EXECUTE_CONTEXT", []driver.NamedValue{{Value: ctx.Value(customFilterTestingContextKey{}) == true}})
		if len(args) != 2 || !strings.Contains(q, `"custom_filters"."id" =`) || args[1].Value != int64(1) {
			return nil, errors.New("execution lookup lost exact ID or row bound")
		}
		id := args[0].Value.(int64)
		if id == 3 {
			return nil, errors.New("private custom-filter lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "config"}}
		if id == 2147483647 {
			return r, nil
		}
		script := map[int64]string{1: "normal", 5: "syntax-error", 6: "execution-timeout", 7: "too-large", 8: "worker-error", 9: "token-refused", 10: "invalid-json", 11: "truncated-response", 12: "transport-unavailable", 13: "spaced-response"}[id]
		config, _ := json.Marshal(map[string]string{"script": script})
		if id == 2 {
			config = []byte(`{}`)
		}
		if id == 4 {
			config = []byte(`broken JSON`)
		}
		r.rows = [][]driver.Value{{id, config}}
		return r, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func (c *customFilterTestingConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(q, `"custom_filters"`) || strings.Contains(q, `"messages"`) || strings.Contains(q, `"message_traces"`) {
		c.record("EXECUTE_WRITE", args)
	}
	return nil, errors.New("execution preview must not persist changes")
}

func newCustomFilterTestingRouters(t *testing.T) map[string]bulkQueueExampleScenario {
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
		c := &customFilterTestingConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }

		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512, PipelineTestRateLimitEnabled: limited, PipelineTestRateLimitRPS: 1.0 / 3600, PipelineTestRateLimitBurst: 1}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req = req.WithContext(context.WithValue(req.Context(), customFilterTestingContextKey{}, true))
			r.Public.ServeHTTP(w, req)
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	// Explicit fixture cases keep regression tests independent of published examples.
	for _, name := range []string{
		"body_boundary",
		"config_invalid",
		"cookie_session",
		"csrf_mismatch",
		"csrf_missing",
		"domain_scope",
		"email_array",
		"email_nested",
		"email_null",
		"empty_body",
		"invalid_body",
		"invalid_format",
		"invalid_id",
		"invalid_overflow",
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
		"permission_store_unavailable",
		"rate_limited",
		"read_only",
		"request_too_large",
		"script_missing",
		"session_unavailable",
		"sidecar_invalid_json",
		"sidecar_spaced",
		"sidecar_syntax_error",
		"sidecar_timeout",
		"sidecar_token_refused",
		"sidecar_too_large",
		"sidecar_truncated",
		"sidecar_unavailable",
		"sidecar_worker_error",
		"unauthenticated",
		"unknown_field",
		"valid",
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
		warmup := r.Clone(r.Context())
		warmup.Body = io.NopCloser(strings.NewReader("{"))
		warmup.ContentLength = 1
		limited.handler.ServeHTTP(httptest.NewRecorder(), warmup)
		limited.handler.ServeHTTP(w, r)
	}), token}, limited.store}
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

	return out
}

func newCustomFilterTestingScenarios(t *testing.T) (map[string]bulkQueueExampleScenario, *customFilterValidationCalls) {
	t.Helper()
	calls := &customFilterValidationCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fromCustomFilterAPI(r) {
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/execute" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer "+customFilterValidationToken {
			t.Errorf("incorrect execution sidecar wiring: %s %s %q %q from %s ua=%q", r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), r.RemoteAddr, r.UserAgent())
			w.WriteHeader(401)
			return
		}
		var request map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		calls.mu.Lock()
		calls.requests = append(calls.requests, request)
		calls.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch request["script"] {
		case "syntax-error":
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"Unexpected token"}`)
		case "execution-timeout":
			w.WriteHeader(408)
			_, _ = io.WriteString(w, `{"error":"Execution timed out"}`)
		case "too-large":
			w.WriteHeader(413)
			_, _ = io.WriteString(w, `{"error":"Email exceeds sidecar limit"}`)
		case "worker-error":
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"error":"Worker unavailable"}`)
		case "token-refused":
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
		case "invalid-json":
			_, _ = io.WriteString(w, "{")
		case "truncated-response":
			b := `{"result":{},"logs":[]}`
			w.Header().Set("Content-Length", strconv.Itoa(len(b)+11))
			_, _ = io.WriteString(w, b)
		case "transport-unavailable":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
		case "spaced-response":
			_, _ = io.WriteString(w, " {\"result\":{\"custom\":42}, \"logs\":[\"checked\"]}\n")
		default:
			_, _ = io.WriteString(w, `{"result":{"action":"continue"},"logs":[]}`)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("JS_FILTER_URL", server.URL)
	t.Setenv("JS_FILTER_TOKEN", customFilterValidationToken)
	return newCustomFilterTestingRouters(t), calls
}

func TestCustomFilterTestingFailures(t *testing.T) {
	f, _ := newCustomFilterTestingScenarios(t)
	for _, id := range []int{3, 10, 11} {
		t.Run(strconv.Itoa(id), func(t *testing.T) {
			rr := mailboxSessionEndingRequest(t, f["valid"], http.MethodPost, "/api/v1/admin/custom-filters/"+strconv.Itoa(id)+"/test", []byte(`{}`))
			if rr.Code != 500 || !json.Valid(rr.Body.Bytes()) || strings.Contains(rr.Body.String(), "private") {
				t.Fatalf("failure got%d %s want sanitized500", rr.Code, rr.Body)
			}
		})
	}
}

func TestCustomFilterTestingForwardedBytes(t *testing.T) {
	f, _ := newCustomFilterTestingScenarios(t)
	rr := mailboxSessionEndingRequest(t, f["valid"], http.MethodPost, "/api/v1/admin/custom-filters/13/test", []byte(`{}`))
	want := " {\"result\":{\"custom\":42}, \"logs\":[\"checked\"]}\n"
	if rr.Code != 200 || rr.Body.String() != want {
		t.Fatalf("forwarded%d %q want200 %q", rr.Code, rr.Body.String(), want)
	}
}

func TestCustomFilterTestingSidecarIgnoresForeignRequests(t *testing.T) {
	f, calls := newCustomFilterTestingScenarios(t)
	probeCustomFilterSidecar(t)
	if n := len(calls.snapshot()); n != 0 {
		t.Fatalf("probe recorded as sidecar call%d", n)
	}
	rr := mailboxSessionEndingRequest(t, f["valid"], http.MethodPost, "/api/v1/admin/custom-filters/1/test", []byte(`{}`))
	if rr.Code != 200 || len(calls.snapshot()) != 1 {
		t.Fatalf("execution after probe got%d calls%d want200 1", rr.Code, len(calls.snapshot()))
	}
}
