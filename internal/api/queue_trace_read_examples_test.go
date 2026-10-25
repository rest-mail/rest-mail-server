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
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var queueTraceReadPaths = map[string]string{
	"getQueueEntry":   "/api/v1/admin/queue/{id}",
	"getMessageTrace": "/api/v1/admin/messages/{id}/trace",
}

// The real GORM lookup and stored-JSON mapper consume these fixed rows. This
// proves API wiring without asserting PostgreSQL ordering or query semantics.
type queueTraceReadConnection struct{ capabilityExampleConnection }

func (c queueTraceReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c queueTraceReadConnection) Driver() driver.Driver                        { return c }
func (c queueTraceReadConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c queueTraceReadConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("queue/trace read attempted a write")
}
func (c queueTraceReadConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) == 0 {
		return nil, errors.New("resource read lost its ID restriction")
	}
	id := fmt.Sprint(args[0].Value)
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	if id == "3" {
		return nil, errors.New("private queue/trace query detail")
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	switch {
	case strings.Contains(query, `FROM "outbound_queue"`):
		if !strings.Contains(query, `"outbound_queue"."id" =`) {
			return nil, errors.New("queue lookup lost primary ID")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "message_id", "sender", "recipient", "domain", "body_type", "status", "attempts", "last_attempt", "next_attempt", "last_error", "last_error_code", "failure_category", "max_retries", "created_at", "expires_at"}}
		if id != "2147483647" {
			var message, last driver.Value
			status, attempts, code, detail, category := "pending", int64(0), int64(0), "", ""
			if id == "2" {
				message, last, status, attempts, code, detail, category = int64(42), stamp, "deferred", int64(2), int64(451), "Try again later", "greylisted"
			}
			r.rows = [][]driver.Value{{n, message, "sender@example.com", "recipient@destination.test", "destination.test", "8BITMIME", status, attempts, last, stamp.Add(time.Hour), detail, code, category, int64(10), stamp, stamp.Add(24 * time.Hour)}}
		}
		return r, nil
	case strings.Contains(query, `FROM "message_traces"`):
		if !strings.Contains(query, "message_id =") || !strings.Contains(query, "created_at DESC") {
			return nil, errors.New("trace lookup lost delivered-message/latest restriction")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "message_id", "rfc_message_id", "direction", "transport", "mail_from", "rcpt_to", "client_ip", "pipeline_id", "final_action", "outcome", "reason_code", "spam_score", "duration_ms", "stages", "sampled", "created_at", "expires_at"}}
		if id != "2147483647" {
			var stages driver.Value = []byte(`[{"filter_name":"header_validate","filter_type":"action","action":"continue","log":{"filter":"header_validate","result":"pass","detail":"Headers accepted","duration_ms":2},"duration_ms":3}]`)
			if id == "2" {
				stages = nil
			}
			if id == "4" {
				stages = []byte(`{"invalid"`)
			}
			if id == "5" {
				stages = []byte(`[]`)
			}
			r.rows = [][]driver.Value{{n + 100, n, "<fixture@example.com>", "inbound", "tls", "sender@example.com", "recipient@example.com", "192.0.2.10", int64(7), "continue", "delivered", "", float64(2.5), int64(12), stages, true, stamp, stamp.Add(24 * time.Hour)}}
		}
		return r, nil
	}
	return nil, errors.New("unexpected SQL in queue/trace read")
}

func queueTraceReadRouters(t *testing.T) (map[string]http.Handler, *auth.JWTService) {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	pool := sql.OpenDB(queueTraceReadConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	makeRouter := func() http.Handler {
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
		t.Cleanup(r.Close)
		return r.Public
	}
	global := makeRouter()
	previous := heldGrants
	defer func() { heldGrants = previous }()
	scoped := func(entries ...grants.Entry) http.Handler {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) { return grants.HeldOf(entries...), nil }
		}
		return makeRouter()
	}
	minimum := scoped(grants.Entry{Capability: middleware.CapQueueRead, Scope: grants.All}, grants.Entry{Capability: middleware.CapObservabilityRead, Scope: grants.All})
	queueOnly := scoped(grants.Entry{Capability: middleware.CapQueueRead, Scope: grants.All})
	observabilityOnly := scoped(grants.Entry{Capability: middleware.CapObservabilityRead, Scope: grants.All})
	domain := scoped(grants.Entry{Capability: middleware.CapQueueRead, Scope: grants.Domain("example.com")}, grants.Entry{Capability: middleware.CapObservabilityRead, Scope: grants.Domain("example.com")})
	wrong := scoped(grants.Entry{Capability: middleware.CapPipelinesRead, Scope: grants.All}, grants.Entry{Capability: middleware.CapDomainsRead, Scope: grants.All})
	heldGrants = func(*grants.Cache) middleware.HeldGrants {
		return func(uint) (grants.Held, error) { return grants.Held{}, errors.New("private grant-store detail") }
	}
	failure := makeRouter()
	return map[string]http.Handler{
		"found": global, "deferred": global, "stages_null": global, "stages_empty": global, "invalid_stages": global,
		"missing": global, "internal_error": global, "invalid_id": global, "invalid_format": global, "request_too_large": global,
		"unauthenticated": unavailable, "session_unavailable": unavailable, "permission_store_unavailable": failure,
		"required_capability": minimum, "domain_scope": domain, "wrong_capability": wrong,
		"queue_capability": queueOnly, "observability_capability": observabilityOnly,
	}, jwt
}

func TestQueueTraceReadDatabaseFailuresAreNotMissing(t *testing.T) {
	routers, jwt := queueTraceReadRouters(t)
	for _, tc := range []struct{ path, message string }{
		{"/api/v1/admin/queue/3", "Internal server error"},
		{"/api/v1/admin/messages/3/trace", "Failed to get message trace"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
		rr := httptest.NewRecorder()
		routers["found"].ServeHTTP(rr, req)
		if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\""+tc.message+"\"}}\n" {
			t.Errorf("%s: got %d %s, want sanitized500", tc.path, rr.Code, rr.Body.String())
		}
	}
}

func TestQueueTraceReadExamples(t *testing.T) {
	routers, jwt := queueTraceReadRouters(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range queueTraceReadPaths {
		if len(pairs[operation]) < 8 {
			t.Fatalf("incomplete pairs for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				handler, ok := routers[name]
				if !ok {
					t.Fatalf("no scenario for %s", name)
				}
				req := httptest.NewRequest(http.MethodGet, domainAliasReadURI(path, pair), nil)
				if name != "unauthenticated" {
					req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, ok := queueTraceReadPaths[warning.OperationID]; ok {
				t.Errorf("queue/trace warning: %v", warning)
			}
		}
	}
}

func TestQueueTraceReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for queue/trace exchange proof")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers, jwt := queueTraceReadRouters(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range queueTraceReadPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				token := mailboxToken(t, jwt)
				if name == "unauthenticated" {
					token = ""
				}
				checkMailboxLookupWithVertrag(t, binary, routers[name], token, path, name, pair)
			})
		}
	}
}
