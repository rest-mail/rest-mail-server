package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
)

var senderRuleCreatePaths = map[string]string{
	"addToAllowlist": "/api/v1/admin/domains/{id}/allowlist",
	"addToBlocklist": "/api/v1/admin/domains/{id}/blocklist",
}

type senderRuleCreateConnection struct {
	domainPolicyGetConnection
	listType string
	mu       sync.Mutex
	attempts []map[string]driver.Value
	created  int
}

func (c *senderRuleCreateConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *senderRuleCreateConnection) Driver() driver.Driver                        { return c }
func (c *senderRuleCreateConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *senderRuleCreateConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("unexpected sender-rule SQL write")
}

func (c *senderRuleCreateConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.HasPrefix(query, "INSERT ") {
		return c.domainPolicyGetConnection.QueryContext(ctx, query, args)
	}
	parts := domainPolicyInsertColumns.FindStringSubmatch(query)
	if parts == nil || parts[1] != "domain_sender_rules" {
		return nil, errors.New("unrelated SQL insert")
	}
	columns := strings.Split(parts[2], ",")
	if len(columns) != len(args) {
		return nil, errors.New("unexpected sender-rule insert arguments")
	}
	fields := map[string]driver.Value{}
	for i, col := range columns {
		fields[strings.Trim(col, `"`)] = args[i].Value
	}
	if fmt.Sprint(fields["domain_id"]) != "1" || fields["list_type"] != c.listType {
		return nil, errors.New("sender-rule insert lost domain or list type")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts = append(c.attempts, fields)
	switch fields["pattern"] {
	case "already@example.net":
		return nil, &pgconn.PgError{Code: "23505", Message: "duplicate sender rule", ConstraintName: "domain_sender_rule_unique"}
	case "translated@example.net":
		return nil, fmt.Errorf("insert sender rule: %w", gorm.ErrDuplicatedKey)
	case "saving@example.net":
		return nil, errors.New("sensitive sender-rule save detail")
	}
	c.created++
	return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(1)}}}, nil
}

func senderRuleCreateRouters(t *testing.T, operation string) (map[string]http.Handler, *auth.JWTService, *senderRuleCreateConnection) {
	t.Helper()
	configureAuthReadPush(t)
	useRealGrants(t)
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	makeRouter := func(db *gorm.DB, cap int64) http.Handler {
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: cap}, nil)
		t.Cleanup(r.Close)
		return r.Public
	}
	unavailable := makeRouter(newFailingGormDB(t), 4096)
	sessionCheck = func(*handlers.SessionCache) middleware.SessionCurrent {
		return func(uint, int) (bool, error) { return true, nil }
	}
	heldGrants = func(*grants.Cache) middleware.HeldGrants {
		return func(uint) (grants.Held, error) {
			return grants.HeldOf(grants.Entry{Capability: grants.Wildcard, Scope: grants.All}), nil
		}
	}
	store := &senderRuleCreateConnection{listType: "allow"}
	if operation == "addToBlocklist" {
		store.listType = "block"
	}
	pool := sql.OpenDB(store)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent), NowFunc: func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	normal := makeRouter(db, 4096)
	oversized := makeRouter(db, 8)
	scoped := func(domain string) http.Handler {
		previous := heldGrants
		defer func() { heldGrants = previous }()
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				return grants.HeldOf(grants.Entry{Capability: middleware.CapDomainsWrite, Scope: grants.Domain(domain)}), nil
			}
		}
		return makeRouter(db, 4096)
	}
	return map[string]http.Handler{"rate_limited": apiKeyRefusalLimited(t), "normal": normal, "created": scoped("example.com"), "created_without_reason": scoped("example.com"), "outside_domain": scoped("other.test"), "request_too_large": oversized, "unauthenticated": unavailable, "session_unavailable": unavailable}, jwt, store
}

func TestSenderRuleCreateLookupFailuresAreNotMissingDomains(t *testing.T) {
	for operation, path := range senderRuleCreatePaths {
		t.Run(operation, func(t *testing.T) {
			routers, jwt, store := senderRuleCreateRouters(t, operation)
			req := httptest.NewRequest(http.MethodPost, strings.ReplaceAll(path, "{id}", "4"), strings.NewReader(`{"pattern":"sender@example.net"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
			rr := httptest.NewRecorder()
			routers["normal"].ServeHTTP(rr, req)
			if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
				t.Fatalf("domain lookup failure: %d %s", rr.Code, rr.Body.String())
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			if len(store.attempts) != 0 {
				t.Fatal("failed domain lookup attempted an insert")
			}
		})
	}
}

func TestSenderRuleCreateExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range senderRuleCreatePaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				routers, jwt, store := senderRuleCreateRouters(t, operation)
				id := "1"
				if value, ok := pair.Request.Path["id"]; ok {
					id = fmt.Sprint(value)
					if number, ok := value.(float64); ok {
						id = strconv.FormatFloat(number, 'f', -1, 64)
					}
				}
				req := httptest.NewRequest(http.MethodPost, strings.ReplaceAll(path, "{id}", id), strings.NewReader(string(pair.Request.Body)))
				req.Header.Set("Content-Type", "application/json")
				if name != "unauthenticated" {
					req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
				}
				precondition := name
				if routers[precondition] == nil {
					precondition = "normal"
				}
				rr := httptest.NewRecorder()
				routers[precondition].ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d, want %d: %s", rr.Code, pair.Response.Status, rr.Body.String())
				}
				media := pair.Response.MediaType
				if media == "" {
					media = "application/json"
				}
				if rr.Header().Get("Content-Type") != media {
					t.Fatal("response media type differs")
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				store.mu.Lock()
				defer store.mu.Unlock()
				wantCreated := 0
				wantAttempts := 0
				if rr.Code == 201 {
					wantCreated = 1
					wantAttempts = 1
				}
				if rr.Code == 409 || name == "save_unavailable" {
					wantAttempts = 1
				}
				if store.created != wantCreated || len(store.attempts) != wantAttempts {
					t.Fatalf("insert boundary: created %d/attempted %d, want %d/%d", store.created, len(store.attempts), wantCreated, wantAttempts)
				}
				if rr.Code == 201 {
					var request map[string]interface{}
					if err := json.Unmarshal(pair.Request.Body, &request); err != nil {
						t.Fatal(err)
					}
					fields := store.attempts[0]
					for _, key := range []string{"pattern", "reason"} {
						want := request[key]
						if want == nil {
							want = ""
						}
						if fields[key] != want {
							t.Errorf("stored %s=%v, request=%v", key, fields[key], want)
						}
					}
				}
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, ok := senderRuleCreatePaths[warning.OperationID]; ok {
				t.Errorf("sender create operation still has warning: %v", warning)
			}
		}
	}
}

func TestSenderRuleCreateExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for sender-rule create replay")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range senderRuleCreatePaths {
		counts := map[string]int{}
		for _, pair := range pairs[operation] {
			counts[fmt.Sprint(pair.Response.Status)]++
		}
		for status, count := range counts {
			t.Run(operation+"/"+status, func(t *testing.T) {
				routers, jwt, _ := senderRuleCreateRouters(t, operation)
				token := mailboxToken(t, jwt)
				precondition := map[string]string{"201": "created", "401": "unauthenticated", "403": "outside_domain", "413": "request_too_large", "503": "session_unavailable"}[status]
				if precondition == "" {
					precondition = "normal"
				}
				if status == "401" {
					token = ""
				}
				checkRegisteredExamplesWithVertrag(t, binary, routers[precondition], token, http.MethodPost, status, []string{path}, count, false)
			})
		}
	}
}
