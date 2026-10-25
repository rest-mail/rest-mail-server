package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
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
func (c adminResourceDeleteConnection) Driver() driver.Driver                        { return c }
func (c adminResourceDeleteConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c adminResourceDeleteConnection) Begin() (driver.Tx, error)                    { return bulkQueueExampleTx{}, nil }

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
func TestAdminResourceDeleteExamples(t *testing.T) {
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range adminResourceDeleteExamples {
		scenarios := adminResourceDeleteScenarios(t, operation)
		if len(pairs[operation]) < 12 {
			t.Fatalf("incomplete %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				var before int
				var reads int64
				if s.store != nil {
					before = len(s.store.snapshot())
					reads = s.reads.Load()
				}
				req := httptest.NewRequest(http.MethodDelete, messageCollectionReadURI(route.path, pair), nil)
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s, want %d", rr.Code, rr.Body.String(), pair.Response.Status)
				}
				if rr.Code == 204 {
					if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" {
						t.Fatal("204 carried content")
					}
				} else {
					if rr.Header().Get("Content-Type") != "application/json" {
						t.Fatal("non-JSON resource answer")
					}
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
				var mutations []bulkQueueExampleMutation
				if s.store != nil {
					mutations = s.store.snapshot()[before:]
				}
				wantWrite := rr.Code < 300 || name == "internal_error" || (operation != "deleteCertificate" && name == "missing")
				if (len(mutations) == 1) != wantWrite || len(mutations) > 1 {
					t.Fatalf("delete attempts=%d for %s/%s", len(mutations), operation, name)
				}
				if s.reads != nil {
					wantReads := int64(0)
					if operation == "deleteCertificate" && (rr.Code < 300 || rr.Code == 404 || name == "internal_error" || name == "lookup_unavailable") {
						wantReads = 1
					}
					if s.reads.Load()-reads != wantReads {
						t.Fatalf("lookup count=%d, want %d", s.reads.Load()-reads, wantReads)
					}
				}
				if wantWrite {
					m := mutations[0]
					id := int64(pair.Request.Path["id"].(float64))
					if len(m.args) != 1 || m.args[0].Value != id || !strings.HasPrefix(m.query, `DELETE FROM "`+route.table+`"`) {
						t.Fatalf("delete target changed: %s %v", m.query, m.args)
					}
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := adminResourceDeleteExamples[w.OperationID]; ok {
				t.Errorf("admin deletion warning: %v", w)
			}
		}
	}
}
func TestAdminResourceDeleteExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for resource delete exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range adminResourceDeleteExamples {
		scenarios := adminResourceDeleteScenarios(t, operation)
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				checkQueueMutationPairWithVertrag(t, binary, scenarios[name].bulkQueueExampleScenario, http.MethodDelete, route.path, name, pair)
			})
		}
	}
}
