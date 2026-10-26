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
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/pipeline/filters"
)

var pipelineTestingPaths = map[string]string{"testFilter": "/api/v1/admin/pipelines/test-filter", "testPipeline": "/api/v1/admin/pipelines/test"}

type pipelineTestingContextKey struct{}

type pipelineTestingFault struct {
	mode   string
	cancel context.CancelFunc
}

func (pipelineTestingFault) Name() string { return "header_validate" }

func (pipelineTestingFault) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f pipelineTestingFault) Execute(ctx context.Context, _ *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	if f.mode == "execution_panic" {
		panic("private filter runtime panic")
	}
	if f.mode == "execution_cancelled" {
		f.cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, errors.New("private filter runtime failure")
}

type pipelineTestingConnection struct {
	domainMutationConnection
	cancel context.CancelFunc
}

func (c *pipelineTestingConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *pipelineTestingConnection) Driver() driver.Driver { return c }

func (c *pipelineTestingConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *pipelineTestingConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, `FROM "pipelines"`) {
		c.record(q, args)
		c.record("PREVIEW_CONTEXT", []driver.NamedValue{{Value: ctx.Value(pipelineTestingContextKey{}) == true}})
		if len(args) != 2 || !strings.Contains(q, `"pipelines"."id" =`) || args[1].Value != int64(1) {
			return nil, errors.New("pipeline preview lost exact ID or row bound")
		}
		id := args[0].Value.(int64)
		if id == 3 {
			return nil, errors.New("private pipeline preview lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "direction", "filters", "active", "created_at", "updated_at"}}
		if id == 2147483647 {
			return r, nil
		}
		config := []byte(`[{"name":"header_validate","type":"action","enabled":true,"config":{}}]`)
		if id == 2 {
			config = []byte(`[]`)
		}
		if id == 4 {
			config = []byte(`broken JSON`)
		}
		r.rows = [][]driver.Value{{id, int64(1), "inbound", config, true, pipelineMutationTime, pipelineMutationTime}}
		return r, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func (c *pipelineTestingConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(q, `"pipelines"`) || strings.Contains(q, `"messages"`) || strings.Contains(q, `"message_traces"`) {
		c.record("PREVIEW_WRITE", args)
	}
	return nil, errors.New("pipeline preview must not persist changes")
}

func newPipelineTestingScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
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
		c := &pipelineTestingConnection{domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }

		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8192, PipelineTestRateLimitEnabled: limited, PipelineTestRateLimitRPS: 1.0 / 3600, PipelineTestRateLimitBurst: 1}, nil)
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
			req = req.WithContext(context.WithValue(req.Context(), pipelineTestingContextKey{}, true))
			r.Public.ServeHTTP(w, req)
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	// Explicit fixture cases keep regression tests independent of published examples.
	for _, name := range []string{
		"accepted_email",
		"body_boundary",
		"config_array",
		"config_missing",
		"config_null",
		"config_unreadable",
		"cookie_session",
		"csrf_mismatch",
		"csrf_missing",
		"domain_scope",
		"email_missing",
		"empty_pipeline",
		"execution_cancelled",
		"execution_failure",
		"execution_panic",
		"filter_name_missing",
		"filter_name_wrong_type",
		"invalid_body",
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
		"pipeline_id_wrong_type",
		"rate_limited",
		"read_only",
		"rejected_email",
		"request_too_large",
		"session_unavailable",
		"unauthenticated",
		"unknown_filter",
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

	out["cancelled_after_lookup"] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Set("X-Example-Cancel", "after-pipeline-lookup")
		normal.handler.ServeHTTP(w, r)
	}), token}, normal.store}

	for _, mode := range []string{"execution_failure", "execution_panic", "execution_cancelled"} {
		mode := mode
		out[mode] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			pipeline.DefaultRegistry.Register("header_validate", func([]byte) (pipeline.Filter, error) { return pipelineTestingFault{mode: mode, cancel: cancel}, nil })
			defer pipeline.DefaultRegistry.Register("header_validate", filters.NewHeaderValidate)
			normal.handler.ServeHTTP(w, r)
		}), token}, normal.store}
	}
	return out
}

func TestPipelineTestingLookupAndExecutionFailures(t *testing.T) {
	f := newPipelineTestingScenarios(t)
	rr := mailboxSessionEndingRequest(t, f["write_only"], http.MethodPost, pipelineTestingPaths["testPipeline"], []byte(`{"pipeline_id":3,"email":{}}`))
	if rr.Code != 500 {
		t.Errorf("lookup got%d want500", rr.Code)
	}
	for _, name := range []string{"execution_failure", "execution_panic", "execution_cancelled"} {
		t.Run(name, func(t *testing.T) {
			rr := mailboxSessionEndingRequest(t, f[name], http.MethodPost, pipelineTestingPaths["testFilter"], []byte(`{"filter_name":"header_validate","config":{},"email":{}}`))
			if rr.Code != 500 {
				t.Fatalf("runtime got%d want500", rr.Code)
			}
			if !json.Valid(rr.Body.Bytes()) || strings.Contains(rr.Body.String(), "private filter") {
				t.Fatal("runtime error escaped the typed sanitized boundary")
			}
		})
	}
}
