package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
)

var adminResourceDeleteExamples = map[string]struct{ path, table, capability, readCapability string }{
	"deleteCertificate":  {"/api/v1/admin/certificates/{id}", "certificates", middleware.CapDomainsWrite, middleware.CapDomainsRead},
	"deletePipeline":     {"/api/v1/admin/pipelines/{id}", "pipelines", middleware.CapPipelinesDel, middleware.CapPipelinesRead},
	"deleteCustomFilter": {"/api/v1/admin/custom-filters/{id}", "custom_filters", middleware.CapPipelinesDel, middleware.CapPipelinesRead},
}

type adminResourceDeleteConnection struct {
	capabilityExampleConnection
	store *bulkQueueExampleStore
	reads *atomic.Int64
	table string
	fail  bool
}

func (c adminResourceDeleteConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c adminResourceDeleteConnection) Driver() driver.Driver { return c }

func (c adminResourceDeleteConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c adminResourceDeleteConnection) Begin() (driver.Tx, error) { return bulkQueueExampleTx{}, nil }

// Fixed rows and affected counts exercise actual GORM selectors and HTTP
// adapters. No live resource is removed; database constraints, transactions
// and persistence remain the component/CI tests' responsibility.
func (c adminResourceDeleteConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.table != "certificates" || !strings.Contains(query, `FROM "certificates"`) {
		return nil, errors.New("unexpected resource delete lookup")
	}
	c.reads.Add(1)
	if len(args) != 2 || !strings.Contains(query, `"certificates"."id" =`) || args[1].Value != int64(1) {
		return nil, errors.New("certificate delete lost ID or bound")
	}
	id := fmt.Sprint(args[0].Value)
	if id == "3" {
		return nil, errors.New("private certificate lookup detail")
	}
	r := &authReadSnapshotRows{columns: []string{"id"}}
	if id == "2147483647" {
		return r, nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	r.rows = [][]driver.Value{{n}}
	return r, nil
}

func (c adminResourceDeleteConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, `DELETE FROM "`+c.table+`"`) || !strings.Contains(query, "WHERE") || !strings.Contains(query, `"id"`) || len(args) != 1 {
		return nil, errors.New("resource delete lost table or exact primary key")
	}
	c.store.mu.Lock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{query, append([]driver.NamedValue(nil), args...)})
	c.store.mu.Unlock()
	if c.fail {
		return nil, errors.New("private resource delete detail")
	}
	if args[0].Value == int64(2147483647) {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

func adminResourceDeleteScenarios(t *testing.T, operation string) map[string]queueEntryActionScenario {
	t.Helper()
	route := adminResourceDeleteExamples[operation]
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previous := heldGrants
	defer func() { heldGrants = previous }()
	makeRouter := func(entries []grants.Entry, permissionFail, writeFail bool) queueEntryActionScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if permissionFail {
					return grants.Held{}, errors.New("private resource permission detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store, reads := &bulkQueueExampleStore{}, &atomic.Int64{}
		pool := sql.OpenDB(adminResourceDeleteConnection{store: store, reads: reads, table: route.table, fail: writeFail})
		t.Cleanup(func() { _ = pool.Close() })
		r := NewRouters(openAccountReadDB(t, pool), jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
		t.Cleanup(r.Close)
		return queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{r.Public, token}, store}, reads}
	}
	all := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(all, false, false)
	out := map[string]queueEntryActionScenario{}
	for _, name := range []string{"found", "missing", "lookup_unavailable", "invalid_id", "invalid_format", "request_too_large"} {
		out[name] = normal
	}
	out["internal_error"] = makeRouter(all, false, true)
	out["required_capability"] = makeRouter([]grants.Entry{{Capability: route.capability, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: route.capability, Scope: grants.Domain("example.com")}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: route.readCapability, Scope: grants.All}}, false, false)
	other := middleware.CapDomainsWrite
	if operation == "deleteCertificate" {
		other = middleware.CapPipelinesDel
	}
	out["other_capability"] = makeRouter([]grants.Entry{{Capability: other, Scope: grants.All}}, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false)
	out["unauthenticated"] = queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}, nil}
	out["session_unavailable"] = queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}, nil}
	out["rate_limited"] = queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{apiKeyRefusalLimited(t), ""}, nil}, nil}
	return out
}

func TestCertificateDeleteLookupFailureIsNotMissing(t *testing.T) {
	s := adminResourceDeleteScenarios(t, "deleteCertificate")["lookup_unavailable"]
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/certificates/3", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)
	if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
		t.Fatalf("got %d %s, want sanitized 500", rr.Code, rr.Body.String())
	}
	if len(s.store.snapshot()) != 0 {
		t.Fatal("failed lookup attempted deletion")
	}
}
