package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
)

var banRemovalPaths = map[string]string{"deleteBan": "/api/v1/admin/bans/{id}", "unbanIP": "/api/v1/admin/bans/ip/{ip}"}

type banRemovalConnection struct {
	capabilityExampleConnection
	store *bulkQueueExampleStore
	fail  bool
}

func (c banRemovalConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c banRemovalConnection) Driver() driver.Driver                        { return c }
func (c banRemovalConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c banRemovalConnection) Begin() (driver.Tx, error)                    { return bulkQueueExampleTx{}, nil }

// Capture actual ID/address deletion predicates; fixed affected counts prove
// wiring, not database persistence, protocol-ban caches or firewall behavior.
func (c banRemovalConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, `DELETE FROM "bans"`) || !strings.Contains(query, "WHERE") || len(args) != 1 {
		return nil, errors.New("ban deletion lost exact selector")
	}
	c.store.mu.Lock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{query, append([]driver.NamedValue(nil), args...)})
	c.store.mu.Unlock()
	if c.fail {
		return nil, errors.New("private ban deletion detail")
	}
	if args[0].Value == int64(2147483647) || args[0].Value == "192.0.2.99" || args[0].Value == "not-an-ip" {
		return driver.RowsAffected(0), nil
	}
	if strings.Contains(query, "ip =") {
		return driver.RowsAffected(3), nil
	}
	return driver.RowsAffected(1), nil
}
func banRemovalScenarios(t *testing.T) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previous := heldGrants
	defer func() { heldGrants = previous }()
	makeRouter := func(entries []grants.Entry, permissionFail, writeFail bool) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if permissionFail {
					return grants.Held{}, errors.New("private ban permission detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		pool := sql.OpenDB(banRemovalConnection{store: store, fail: writeFail})
		t.Cleanup(func() { _ = pool.Close() })
		r := NewRouters(openAccountReadDB(t, pool), jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{r.Public, token}, store}
	}
	all := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(all, false, false)
	out := map[string]bulkQueueExampleScenario{}
	for _, name := range []string{"found", "missing", "ipv6", "literal_address", "invalid_id", "invalid_format", "request_too_large"} {
		out[name] = normal
	}
	out["internal_error"] = makeRouter(all, false, true)
	out["required_capability"] = makeRouter([]grants.Entry{{Capability: middleware.CapBansDelete, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapBansDelete, Scope: grants.Domain("example.com")}}, false, false)
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapBansRead, Scope: grants.All}}, false, false)
	out["other_capability"] = makeRouter([]grants.Entry{{Capability: middleware.CapQueueManage, Scope: grants.All}}, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false)
	out["unauthenticated"] = bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}
	out["session_unavailable"] = bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}
	return out
}
func TestUnbanFailureIsNotMissing(t *testing.T) {
	s := banRemovalScenarios(t)["internal_error"]
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/bans/ip/192.0.2.10", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)
	if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
		t.Fatalf("got %d %s, want sanitized 500", rr.Code, rr.Body.String())
	}
}
func TestBanRemovalExamples(t *testing.T) {
	scenarios := banRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range banRemovalPaths {
		if len(pairs[operation]) < 10 {
			t.Fatalf("incomplete %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				before := 0
				if s.store != nil {
					before = len(s.store.snapshot())
				}
				req := httptest.NewRequest(http.MethodDelete, messageCollectionReadURI(path, pair), nil)
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
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
				var calls []bulkQueueExampleMutation
				if s.store != nil {
					calls = s.store.snapshot()[before:]
				}
				wantWrite := rr.Code == 204 || name == "missing" || name == "literal_address" || name == "internal_error"
				if (len(calls) == 1) != wantWrite || len(calls) > 1 {
					t.Fatalf("deletion attempts=%d for %s", len(calls), name)
				}
				if !wantWrite {
					return
				}
				m := calls[0]
				if operation == "unbanIP" {
					if !strings.Contains(m.query, "ip =") || m.args[0].Value != pair.Request.Path["ip"] {
						t.Fatalf("wrong address selector: %s %v", m.query, m.args)
					}
				} else if !strings.Contains(m.query, `"id"`) || m.args[0].Value != int64(pair.Request.Path["id"].(float64)) {
					t.Fatal("wrong ID selector")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := banRemovalPaths[w.OperationID]; ok {
				t.Errorf("ban removal warning: %v", w)
			}
		}
	}
}
func TestUnbanHasNoTransportValidationRefusal(t *testing.T) {
	s := banRemovalScenarios(t)["found"]
	for _, path := range []string{"/api/v1/admin/bans/ip/"} {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		rr := httptest.NewRecorder()
		s.handler.ServeHTTP(rr, req)
		if rr.Code != 404 {
			t.Fatalf("absent route segment: %d %s", rr.Code, rr.Body.String())
		}
	}
	// The shorter URL matches the separate numeric ban-ID operation.
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/bans/ip", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)
	if rr.Code != 422 || !strings.Contains(rr.Body.String(), `"id":"must be a non-negative integer"`) {
		t.Fatalf("ban-ID route boundary: %d %s", rr.Code, rr.Body.String())
	}
	doc := contractDocument(t, describedRouters(t).generator("unban transport"))
	responses := doc["paths"].(map[string]interface{})[banRemovalPaths["unbanIP"]].(map[string]interface{})["delete"].(map[string]interface{})["responses"].(map[string]interface{})
	if _, present := responses["422"]; present {
		t.Fatal("plain nonempty path string declared unreachable 422")
	}
	// The same transport rule applies when push-device removal is configured.
	seenDevice := false
	for _, routers := range gapRouters(t) {
		profile := contractDocument(t, routers.generator("conditional path binding"))
		for _, path := range profile["paths"].(map[string]interface{}) {
			for _, operation := range path.(map[string]interface{}) {
				op, ok := operation.(map[string]interface{})
				if !ok || op["operationId"] != "removePushDevice" {
					continue
				}
				seenDevice = true
				if _, present := op["responses"].(map[string]interface{})["422"]; present {
					t.Fatal("conditional plain path declared unreachable 422")
				}
			}
		}
	}
	if !seenDevice {
		t.Fatal("push-device removal profile was not described")
	}
}
func TestBanRemovalExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for ban removal exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	s := banRemovalScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range banRemovalPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				checkQueueMutationPairWithVertrag(t, binary, s[name], http.MethodDelete, path, name, pair)
			})
		}
	}
}
