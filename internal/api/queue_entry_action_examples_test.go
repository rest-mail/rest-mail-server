package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
)

var queueEntryActionExamples = map[string]struct{ method, path string }{"retryQueueEntry": {http.MethodPost, "/api/v1/admin/queue/{id}/retry"}, "bounceQueueEntry": {http.MethodPost, "/api/v1/admin/queue/{id}/bounce"}, "deleteQueueEntry": {http.MethodDelete, "/api/v1/admin/queue/{id}"}}

type queueEntryActionConnection struct {
	bulkQueueExampleConnection
	reads *atomic.Int64
}

func (c queueEntryActionConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c queueEntryActionConnection) Driver() driver.Driver { return c }

func (c queueEntryActionConnection) Open(string) (driver.Conn, error) { return c, nil }

// Fixed SQL snapshots and mutation results prove the action adapters and
// authorization boundary, not PostgreSQL transactions, races or persistence.
func (c queueEntryActionConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, `FROM "outbound_queue"`) {
		return nil, errors.New("unexpected queue entry query")
	}
	c.reads.Add(1)
	if len(args) != 2 || !strings.Contains(query, `"outbound_queue"."id" =`) || args[1].Value != int64(1) {
		return nil, errors.New("queue lookup lost ID or single-row bound")
	}
	id := fmt.Sprint(args[0].Value)
	if id == "3" {
		return nil, errors.New("private queue lookup detail")
	}
	r := &authReadSnapshotRows{columns: []string{"id", "status"}}
	if id == "2147483647" {
		return r, nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	status := map[string]string{"2": "deferred", "4": "delivered", "5": "bounced", "6": "expired", "7": "delivering"}[id]
	if status == "" {
		status = "pending"
	}
	r.rows = [][]driver.Value{{n, status}}
	return r, nil
}

func (c queueEntryActionConnection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.bulkQueueExampleConnection.ExecContext(ctx, query, args); err != nil {
		return nil, err
	}
	if strings.HasPrefix(query, `DELETE FROM "outbound_queue"`) {
		for _, arg := range args {
			if arg.Value == int64(2147483647) {
				return driver.RowsAffected(0), nil
			}
		}
	}
	return driver.RowsAffected(1), nil
}

func queueEntryActionScenarios(t *testing.T) map[string]queueEntryActionScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previous := heldGrants
	defer func() { heldGrants = previous }()
	makeRouter := func(entries []grants.Entry, permissionsFail, writeFail bool) queueEntryActionScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if permissionsFail {
					return grants.Held{}, errors.New("private queue permission detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store, reads := &bulkQueueExampleStore{}, &atomic.Int64{}
		pool := sql.OpenDB(queueEntryActionConnection{bulkQueueExampleConnection{store: store, fail: writeFail}, reads})
		t.Cleanup(func() { _ = pool.Close() })
		r := NewRouters(openAccountReadDB(t, pool), jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
		t.Cleanup(r.Close)
		return queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{r.Public, token}, store}, reads}
	}
	all := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(all, false, false)
	out := map[string]queueEntryActionScenario{}
	for _, name := range []string{"found", "deferred", "already_delivering", "already_delivered", "already_bounced", "expired", "missing", "lookup_unavailable", "invalid_id", "invalid_format", "request_too_large"} {
		out[name] = normal
	}
	out["internal_error"] = makeRouter(all, false, true)
	out["required_capability"] = makeRouter([]grants.Entry{{Capability: middleware.CapQueueManage, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapQueueManage, Scope: grants.Domain("example.com")}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapQueueRead, Scope: grants.All}}, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false)
	out["unauthenticated"] = queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}, nil}
	out["session_unavailable"] = queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}, nil}
	setup, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	out["setup_required"] = queueEntryActionScenario{bulkQueueExampleScenario{accountReadScenario{normal.handler, setup.AccessToken}, normal.store}, normal.reads}
	return out
}

func TestQueueEntryActionExamples(t *testing.T) {
	scenarios := queueEntryActionScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range queueEntryActionExamples {
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
				started := time.Now()
				req := httptest.NewRequest(route.method, messageCollectionReadURI(route.path, pair), nil)
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
						t.Fatalf("204 carried content: %v %q", rr.Header(), rr.Body.String())
					}
				} else {
					if rr.Header().Get("Content-Type") != "application/json" {
						t.Fatal("non-JSON action answer")
					}
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
				var mutations []bulkQueueExampleMutation
				if s.store != nil {
					mutations = s.store.snapshot()[before:]
				}
				wantWrite := rr.Code < 300 || name == "internal_error" || (operation == "deleteQueueEntry" && name == "missing")
				if (len(mutations) == 1) != wantWrite || len(mutations) > 1 {
					t.Fatalf("mutation attempts=%d for %s/%s", len(mutations), operation, name)
				}
				lookupExpected := operation != "deleteQueueEntry" && (rr.Code < 300 || rr.Code == 400 || rr.Code == 404 || name == "lookup_unavailable" || name == "internal_error")
				if s.reads != nil {
					wantReads := int64(0)
					if lookupExpected {
						wantReads = 1
					}
					if s.reads.Load()-reads != wantReads {
						t.Fatalf("lookup count=%d, want %d", s.reads.Load()-reads, wantReads)
					}
				}
				if !wantWrite {
					return
				}
				m := mutations[0]
				id := int64(pair.Request.Path["id"].(float64))
				has := func(want interface{}) bool {
					for _, a := range m.args {
						if a.Value == want {
							return true
						}
					}
					return false
				}
				if !strings.Contains(m.query, "WHERE") || !strings.Contains(m.query, `"id"`) || !has(id) {
					t.Fatalf("mutation lost exact target: %s %v", m.query, m.args)
				}
				if operation == "deleteQueueEntry" {
					if !strings.HasPrefix(m.query, `DELETE FROM "outbound_queue"`) {
						t.Fatal("wrong deletion target")
					}
					return
				}
				if !strings.HasPrefix(m.query, `UPDATE "outbound_queue" SET`) {
					t.Fatal("wrong update target")
				}
				if operation == "retryQueueEntry" {
					if !has("pending") {
						t.Fatal("retry did not restore pending")
					}
					index := regexp.MustCompile(`"next_attempt"\s*=\s*\$(\d+)`).FindStringSubmatch(m.query)
					if len(index) != 2 {
						t.Fatal("retry lost next attempt")
					}
					n, err := strconv.Atoi(index[1])
					if err != nil || n < 1 || n > len(m.args) {
						t.Fatal("invalid retry placeholder")
					}
					stamp, ok := m.args[n-1].Value.(time.Time)
					if !ok || stamp.Before(started) || stamp.After(time.Now()) {
						t.Fatal("retry timestamp outside request")
					}
				} else if !has("bounced") || !has("manually bounced by admin") {
					t.Fatal("bounce lost status or reason")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := queueEntryActionExamples[w.OperationID]; ok {
				t.Errorf("queue entry action warning: %v", w)
			}
		}
	}
}

func TestQueueEntryActionExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for queue entry action exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := queueEntryActionScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, route := range queueEntryActionExamples {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				checkQueueMutationPairWithVertrag(t, binary, scenarios[name].bulkQueueExampleScenario, route.method, route.path, name, pair)
			})
		}
	}
}
