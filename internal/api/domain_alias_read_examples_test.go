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

var domainAliasReadPaths = map[string]string{
	"getDomain": "/api/v1/admin/domains/{id}",
	"getAlias":  "/api/v1/admin/aliases/{id}",
}

// Production scope/repository/preload queries still run through GORM. These
// controlled SQL rows prove wiring, not PostgreSQL query semantics.
type domainAliasReadConnection struct{ capabilityExampleConnection }

func (c domainAliasReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c domainAliasReadConnection) Driver() driver.Driver                        { return c }
func (c domainAliasReadConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c domainAliasReadConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("resource read attempted a write")
}
func (c domainAliasReadConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) == 0 {
		return nil, errors.New("resource read lost its ID restriction")
	}
	id := fmt.Sprint(args[0].Value)
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	switch {
	case strings.Contains(query, `FROM "aliases"`):
		if strings.Contains(query, "JOIN domains") {
			if !strings.Contains(query, "aliases.id =") {
				return nil, errors.New("alias scope lost alias ID")
			}
			if id == "8" {
				return nil, errors.New("sensitive alias permission detail")
			}
			r := &authReadSnapshotRows{columns: []string{"name"}}
			if id != "2147483647" {
				r.rows = [][]driver.Value{{"example.com"}}
			}
			return r, nil
		}
		if !strings.Contains(query, `"aliases"."id" =`) {
			return nil, errors.New("alias lookup lost ID")
		}
		if id == "3" {
			return nil, errors.New("sensitive alias lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "source_address", "destination_address", "active", "created_at"}}
		if id != "2147483647" {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return nil, err
			}
			domainID, local := int64(1), "support"
			if id == "2" {
				local = "quiet"
			}
			if id == "4" {
				domainID = 4
			}
			r.rows = [][]driver.Value{{n, domainID, local + "@example.com", "owned@destination.test", id != "2", stamp}}
		}
		return r, nil
	case strings.Contains(query, `FROM "domains"`):
		if strings.Contains(query, `SELECT "name"`) {
			if id == "8" {
				return nil, errors.New("sensitive domain permission detail")
			}
			r := &authReadSnapshotRows{columns: []string{"name"}}
			if id != "2147483647" {
				name := "example.com"
				if id == "2" {
					name = "inactive.test"
				}
				r.rows = [][]driver.Value{{name}}
			}
			return r, nil
		}
		if !strings.Contains(query, `"domains"."id" =`) {
			return nil, errors.New("domain lookup lost ID")
		}
		if id == "3" || id == "4" {
			return nil, errors.New("sensitive domain lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "name", "server_type", "active", "default_quota_bytes", "dkim_selector", "dkim_private_key", "sending_policy", "dmarc_policy", "created_at", "updated_at"}}
		if id != "2147483647" {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return nil, err
			}
			name := "example.com"
			if id == "2" {
				name = "inactive.test"
			}
			r.rows = [][]driver.Value{{n, name, "traditional", id != "2", int64(1073741824), "restmail", "private-domain-fixture", "bulk", "reject", stamp, stamp}}
		}
		return r, nil
	}
	return nil, fmt.Errorf("unexpected resource query: %s", query)
}

func domainAliasReadRouters(t *testing.T) (map[string]http.Handler, *auth.JWTService) {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	pool := sql.OpenDB(domainAliasReadConnection{})
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
	domainRead := grants.Entry{Capability: middleware.CapDomainsRead, Scope: grants.Domain("example.com")}
	aliasRead := grants.Entry{Capability: middleware.CapMailboxesRead, Scope: grants.Domain("example.com")}
	return map[string]http.Handler{
		"found": global, "inactive": global, "missing": global, "internal_error": global, "domain_unavailable": global,
		"unauthenticated": unavailable, "session_unavailable": unavailable, "request_too_large": global, "invalid_id": global, "invalid_format": global,
		"domain_scope": scoped(domainRead, aliasRead), "domain_redacted": scoped(aliasRead),
		"outside_domain":               scoped(grants.Entry{Capability: middleware.CapDomainsRead, Scope: grants.Domain("other.test")}, grants.Entry{Capability: middleware.CapMailboxesRead, Scope: grants.Domain("other.test")}),
		"destination_mailbox_scope":    scoped(grants.Entry{Capability: middleware.CapMailboxesRead, Scope: grants.Mailbox("owned@destination.test")}),
		"source_mailbox_scope":         scoped(grants.Entry{Capability: middleware.CapMailboxesRead, Scope: grants.Mailbox("support@example.com")}),
		"permission_store_unavailable": scoped(domainRead, aliasRead),
	}, jwt
}

func TestDomainAliasReadDatabaseFailuresAreNotMissing(t *testing.T) {
	routers, jwt := domainAliasReadRouters(t)
	token := mailboxToken(t, jwt)
	for _, tc := range []struct{ path, message string }{
		{"/api/v1/admin/domains/3", "Internal server error"},
		{"/api/v1/admin/aliases/3", "Failed to get alias"},
		{"/api/v1/admin/aliases/4", "Failed to get alias"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		routers["found"].ServeHTTP(rr, req)
		if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\""+tc.message+"\"}}\n" {
			t.Errorf("%s: got %d %s, want sanitized500", tc.path, rr.Code, rr.Body.String())
		}
	}
}

func domainAliasReadURI(path string, pair oapifly.ExamplePair) string {
	value := pair.Request.Path["id"]
	id := fmt.Sprint(value)
	if number, ok := value.(float64); ok {
		id = strconv.FormatFloat(number, 'f', -1, 64)
	}
	return strings.ReplaceAll(path, "{id}", id)
}

func TestDomainAliasReadExamples(t *testing.T) {
	routers, jwt := domainAliasReadRouters(t)
	token := mailboxToken(t, jwt)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range domainAliasReadPaths {
		if len(pairs[operation]) < 8 {
			t.Fatalf("complete pairs missing for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				handler, ok := routers[name]
				if !ok {
					t.Fatalf("no real scenario for %s", name)
				}
				req := httptest.NewRequest(http.MethodGet, domainAliasReadURI(path, pair), nil)
				if name != "unauthenticated" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d, want %d: %s", rr.Code, pair.Response.Status, rr.Body.String())
				}
				if rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatal("response media differs")
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, ok := domainAliasReadPaths[warning.OperationID]; ok {
				t.Errorf("resource read still has warning: %v", warning)
			}
		}
	}
}

func TestDomainAliasReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for resource-read exchange proof")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers, jwt := domainAliasReadRouters(t)
	token := mailboxToken(t, jwt)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range domainAliasReadPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				credential := token
				if name == "unauthenticated" {
					credential = ""
				}
				// Reuse the registered-GET replay: each scenario sends the canonical
				// URI/transport bytes and keeps the actual schema/expected response.
				checkMailboxLookupWithVertrag(t, binary, routers[name], credential, path, name, pair)
			})
		}
	}
}
