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
	"testing/fstest"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

var adminVerificationPaths = map[string]string{"verifyAuditLog": "/api/v1/admin/audit/verify", "verifyDelivery": "/api/v1/admin/test/verify"}

// Real verifier/handler code consumes these fixed rows. SQL query predicates,
// response wiring and grant middleware are exercised; DB semantics and audit
// append persistence are outside this fixture's proof.
type adminVerificationConnection struct {
	capabilityExampleConnection
	mode string
}

func (c adminVerificationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c adminVerificationConnection) Driver() driver.Driver                        { return c }
func (c adminVerificationConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c adminVerificationConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if strings.Contains(query, `FROM "audit_log"`) {
		if len(args) != 1 || args[0].Value != int64(1000) || !strings.Contains(query, "ORDER BY seq ASC") {
			return nil, errors.New("audit verifier lost ascending bounded traversal")
		}
		if c.mode == "internal_error" {
			return nil, errors.New("private audit query detail")
		}
		r := &authReadSnapshotRows{columns: []string{"seq", "occurred_at", "actor_kind", "action", "outcome", "method", "route", "status", "prev_hash", "hash"}}
		if c.mode == "empty" {
			return r, nil
		}
		prev := audit.GenesisHash
		for seq := int64(1); seq <= 2; seq++ {
			n := seq
			if c.mode == "truncated" {
				n += 4
			}
			if c.mode == "gap" && seq == 2 {
				n = 3
			}
			e := models.AuditEntry{Seq: n, OccurredAt: stamp, ActorKind: "system", Action: "contract.read", Outcome: "success", Method: "GET", Route: "/fixture", Status: 200, PrevHash: prev}
			e.Hash = audit.Hash(&e)
			if c.mode == "broken" && seq == 2 {
				e.Action = "tampered"
			}
			r.rows = append(r.rows, []driver.Value{n, stamp, e.ActorKind, e.Action, e.Outcome, e.Method, e.Route, int64(e.Status), e.PrevHash, e.Hash})
			prev = e.Hash
		}
		return r, nil
	}
	if strings.Contains(query, `FROM "mailboxes"`) {
		if len(args) != 2 || !strings.Contains(query, "address =") || args[0].Value != "recipient@example.com" {
			return nil, errors.New("delivery verification lost recipient restriction")
		}
		if c.mode == "internal_error" {
			return nil, errors.New("private recipient query detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id"}}
		if c.mode != "recipient_missing" {
			r.rows = [][]driver.Value{{int64(10)}}
		}
		return r, nil
	}
	if strings.Contains(query, `FROM "messages"`) {
		if len(args) < 3 || args[0].Value != int64(10) || args[1].Value != "INBOX" || !strings.Contains(query, "ORDER BY received_at DESC") || args[len(args)-1].Value != int64(1) {
			return nil, errors.New("delivery verification lost mailbox, inbox or latest-message restriction")
		}
		if strings.Contains(query, "subject LIKE") && (args[2].Value != "%50\\%\\_\\\\off%" || !strings.Contains(query, "ESCAPE")) {
			return nil, errors.New("delivery verification lost literal subject escaping")
		}
		if strings.Contains(query, "message_id =") && args[len(args)-2].Value != "<verify@example.net>" {
			return nil, errors.New("delivery verification lost exact message identity")
		}
		if c.mode == "message_unavailable" {
			return nil, errors.New("private delivered message query detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "message_id", "subject", "sender", "received_at"}}
		if c.mode != "empty" {
			r.rows = [][]driver.Value{{int64(51), "<verify@example.net>", "50%_\\off", "sender@example.net", stamp}}
		}
		return r, nil
	}
	return nil, errors.New("unexpected verification SQL")
}

func adminVerificationScenarios(t *testing.T) map[string]accountReadScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previous := heldGrants
	defer func() { heldGrants = previous }()
	makeRouter := func(mode string, entries []grants.Entry, fail bool, environment string) *Routers {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if fail {
					return grants.Held{}, errors.New("private verification grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		pool := sql.OpenDB(adminVerificationConnection{mode: mode})
		t.Cleanup(func() { _ = pool.Close() })
		r := NewRouters(openAccountReadDB(t, pool), jwt, &config.Config{Environment: environment, APIMaxBodyBytes: 8}, nil)
		t.Cleanup(r.Close)
		return r
	}
	all := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	out := map[string]accountReadScenario{}
	for _, name := range []string{"found", "empty", "broken", "truncated", "gap", "internal_error", "recipient_missing", "message_unavailable"} {
		out[name] = accountReadScenario{makeRouter(name, all, false, "test").Public, token}
	}
	for _, name := range []string{"anchor_valid", "anchor_uppercase", "anchor_blank", "anchor_mismatch", "anchor_missing", "invalid_anchor", "recipient_required", "recipient_blank", "filtered", "invalid_timeout", "default_timeout", "request_too_large"} {
		out[name] = out["found"]
	}
	out["audit_capability"] = accountReadScenario{makeRouter("found", []grants.Entry{{Capability: middleware.CapAuditRead, Scope: grants.All}}, false, "test").Public, token}
	out["domain_scope"] = accountReadScenario{makeRouter("found", []grants.Entry{{Capability: grants.Wildcard, Scope: grants.Domain("example.com")}}, false, "test").Public, token}
	out["wrong_capability"] = accountReadScenario{makeRouter("found", []grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.All}}, false, "test").Public, token}
	out["permission_store_unavailable"] = accountReadScenario{makeRouter("found", nil, true, "test").Public, token}
	out["unauthenticated"] = accountReadScenario{unavailable, ""}
	out["session_unavailable"] = accountReadScenario{unavailable, token}
	setup, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	out["setup_required"] = accountReadScenario{out["found"].handler, setup.AccessToken}
	normal := out["found"].handler
	out["cookie_session"] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Del("Authorization")
		r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: token})
		normal.ServeHTTP(w, r)
	}), ""}
	production := makeRouter("found", all, false, "production")
	doc := contractDocument(t, production.generator("production verification"))
	if _, ok := doc["paths"].(map[string]interface{})[adminVerificationPaths["verifyDelivery"]]; ok {
		t.Fatal("test verification exposed in production description")
	}
	req := httptest.NewRequest(http.MethodGet, adminVerificationPaths["verifyDelivery"]+"?to=recipient@example.com", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	production.Public.ServeHTTP(rr, req)
	if rr.Code != 404 {
		t.Fatalf("test verification exposed in production: %d", rr.Code)
	}
	return out
}

func TestDeliveryVerificationFailureIsNotANegativeResult(t *testing.T) {
	s := adminVerificationScenarios(t)["message_unavailable"]
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/test/verify?to=recipient@example.com&timeout=0s", nil)
	req.Header.Set("Authorization", "Bearer "+s.token)
	rr := httptest.NewRecorder()
	s.handler.ServeHTTP(rr, req)
	if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
		t.Fatalf("got %d %s, want sanitized 500", rr.Code, rr.Body.String())
	}
}

func TestAdminVerificationExamples(t *testing.T) {
	scenarios := adminVerificationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range adminVerificationPaths {
		if len(pairs[operation]) < 15 {
			t.Fatalf("incomplete examples for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				req := httptest.NewRequest(http.MethodGet, publicDiscoveryURI(path, pair), nil)
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, warning := range g.Report() {
			if _, ok := adminVerificationPaths[warning.OperationID]; ok && gapWarningApplies(map[string]gapOperation{"verifyAuditLog": {}, "verifyDelivery": {}}, warning) {
				t.Errorf("verification warning: %v", warning)
			}
		}
	}
}

func TestGapReportReconcilesConditionalExamplesWithoutHidingFailures(t *testing.T) {
	files := fstest.MapFS{"pairs.json": {Data: []byte(`{"conditional":{"good":{"request":{},"response":{"status":200,"body":{"value":1}}},"bad":{"request":{},"response":{"status":200,"body":{"value":"wrong type"}}}},"unknown":{"old":{"request":{},"response":{"status":200,"body":{}}}}}`)}}
	enabled := oapifly.New(oapifly.Config{Examples: files, ExamplesFile: "pairs.json"})
	enabled.Register(oapifly.Endpoint{Method: "GET", Path: "/conditional", OperationID: "conditional", Responses: []oapifly.EndpointResponse{{Status: 200, Type: oapifly.TypeOf[struct {
		Value int `json:"value"`
	}]()}}})
	disabled := oapifly.New(oapifly.Config{Examples: files, ExamplesFile: "pairs.json"})
	disabled.Register(oapifly.Endpoint{Method: "GET", Path: "/other", OperationID: "other", Responses: []oapifly.EndpointResponse{{Status: 204}}})
	operations := map[string]gapOperation{"conditional": {OperationID: "conditional"}}
	var invalid, unknown, conditionalOrphans int
	for _, generator := range []*oapifly.Generator{disabled, enabled} {
		for _, warning := range generator.Report() {
			if !gapWarningApplies(operations, warning) {
				if warning.OperationID != "conditional" || warning.Kind != "example-orphaned" {
					t.Fatalf("hidden unrelated failure: %+v", warning)
				}
				conditionalOrphans++
				continue
			}
			if warning.Kind == "example-invalid" && warning.OperationID == "conditional" {
				invalid++
			}
			if warning.Kind == "example-orphaned" && warning.OperationID == "unknown" {
				unknown++
			}
		}
	}
	if conditionalOrphans != 2 || invalid != 1 || unknown != 2 {
		t.Fatalf("conditional orphans=%d, invalid=%d, unknown=%d", conditionalOrphans, invalid, unknown)
	}
}

func TestAdminVerificationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for admin verification exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := adminVerificationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range adminVerificationPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s := scenarios[name]
				checkMailboxLookupWithVertrag(t, binary, s.handler, s.token, path, name, pair)
			})
		}
	}
}
