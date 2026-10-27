package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
)

var scopedAdminRemovalPaths = map[string]string{
	"deleteAlias":   "/api/v1/admin/aliases/{id}",
	"deleteDKIMKey": "/api/v1/admin/dkim/{id}",
}

type scopedAdminRemovalConnection struct {
	domainAliasReadConnection
	store     *bulkQueueExampleStore
	reads     *atomic.Int64
	operation string
	fail      bool
}

func (c scopedAdminRemovalConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c scopedAdminRemovalConnection) Driver() driver.Driver { return c }

func (c scopedAdminRemovalConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c scopedAdminRemovalConnection) Begin() (driver.Tx, error) { return bulkQueueExampleTx{}, nil }

func (c scopedAdminRemovalConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.reads.Add(1)
	return c.domainAliasReadConnection.QueryContext(ctx, q, args)
}

// Capture actual GORM writes. Fixed row counts prove wiring, not PostgreSQL
// persistence, key-cache invalidation or live mail routing.
func (c scopedAdminRemovalConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if c.operation == "deleteAlias" {
		if !strings.HasPrefix(q, `DELETE FROM "aliases"`) || !strings.Contains(q, `"id"`) || len(args) != 1 {
			return nil, errors.New("alias deletion lost exact primary key")
		}
	} else {
		if !strings.HasPrefix(q, `UPDATE "domains" SET`) || !strings.Contains(q, `WHERE "id" =`) {
			return nil, errors.New("DKIM removal lost domain primary key")
		}
	}
	c.store.mu.Lock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
	c.store.mu.Unlock()
	if c.fail {
		return nil, errors.New("private scoped removal write detail")
	}
	return driver.RowsAffected(1), nil
}

func scopedAdminRemovalScenarios(t *testing.T, op string) map[string]queueEntryActionScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previous := heldGrants
	defer func() { heldGrants = previous }()
	capability, readCapability := middleware.CapMailboxesDel, middleware.CapMailboxesRead
	if op == "deleteDKIMKey" {
		capability, readCapability = middleware.CapDomainsWrite, middleware.CapDomainsRead
	}
	makeRouter := func(entries []grants.Entry, permissionFail, writeFail bool) queueEntryActionScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if permissionFail {
					return grants.Held{}, errors.New("private scoped permission detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store, reads := &bulkQueueExampleStore{}, &atomic.Int64{}
		pool := sql.OpenDB(scopedAdminRemovalConnection{store: store, reads: reads, operation: op, fail: writeFail})
		t.Cleanup(func() { _ = pool.Close() })
		r := NewRouters(openAccountReadDB(t, pool), jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
		t.Cleanup(r.Close)
		return queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{r.Public, token}, store}, reads}
	}
	normal := makeRouter([]grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}, false, false)
	out := map[string]queueEntryActionScenario{}
	for _, name := range []string{"found", "missing", "lookup_unavailable", "invalid_id", "invalid_format", "request_too_large"} {
		out[name] = normal
	}
	out["internal_error"] = makeRouter([]grants.Entry{{Capability: capability, Scope: grants.All}}, false, true)
	out["required_capability"] = makeRouter([]grants.Entry{{Capability: capability, Scope: grants.All}}, false, false)
	scoped := makeRouter([]grants.Entry{{Capability: capability, Scope: grants.Domain("example.com")}}, false, false)
	for _, name := range []string{"source_domain", "scope_lookup_unavailable", "scoped_missing", "scoped_lookup_unavailable"} {
		out[name] = scoped
	}
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: capability, Scope: grants.Domain("destination.test")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: capability, Scope: grants.Mailbox("support@example.com")}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: readCapability, Scope: grants.All}}, false, false)
	out["other_capability"] = makeRouter([]grants.Entry{{Capability: middleware.CapQueueManage, Scope: grants.All}}, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false)
	out["unauthenticated"] = queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}, nil}
	out["session_unavailable"] = queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}, nil}
	out["rate_limited"] = queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{apiKeyRefusalLimited(t), ""}, nil}, nil}
	return out
}

func TestScopedAdminRemovalLookupFailureIsNotMissing(t *testing.T) {
	for op, path := range scopedAdminRemovalPaths {
		t.Run(op, func(t *testing.T) {
			s := scopedAdminRemovalScenarios(t, op)["lookup_unavailable"]
			req := httptest.NewRequest(http.MethodDelete, strings.ReplaceAll(path, "{id}", "3"), nil)
			req.Header.Set("Authorization", "Bearer "+s.token)
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
				t.Fatalf("got %d %s, want sanitized 500", rr.Code, rr.Body.String())
			}
			if len(s.store.snapshot()) != 0 {
				t.Fatal("failed lookup attempted mutation")
			}
		})
	}
}
