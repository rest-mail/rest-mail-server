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
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
)

var domainPolicyReadPaths = map[string]string{
	"listAllowlist":    "/api/v1/admin/domains/{id}/allowlist",
	"listBlocklist":    "/api/v1/admin/domains/{id}/blocklist",
	"listSuppressions": "/api/v1/admin/domains/{id}/suppressions",
}

type domainPolicyReadConnection struct{ capabilityExampleConnection }

func (c domainPolicyReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c domainPolicyReadConnection) Driver() driver.Driver                        { return c }
func (c domainPolicyReadConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c domainPolicyReadConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) == 0 || fmt.Sprint(args[0].Value) != "1" {
		return nil, errors.New("domain-policy query lost its domain restriction")
	}
	switch {
	case strings.Contains(query, `FROM "domains"`) && strings.Contains(query, `"name"`):
		return &authReadSnapshotRows{columns: []string{"name"}, rows: [][]driver.Value{{"example.com"}}}, nil
	case strings.Contains(query, `FROM "domain_sender_rules"`):
		if len(args) != 2 || (args[1].Value != "allow" && args[1].Value != "block") {
			return nil, errors.New("sender list lost its allow/block restriction")
		}
		return &authReadSnapshotRows{columns: []string{"id"}}, nil
	case strings.Contains(query, `FROM "suppressions"`) && strings.Contains(query, "expires_at >"):
		return &authReadSnapshotRows{columns: []string{"id"}}, nil
	}
	return nil, errors.New("unexpected domain-policy query")
}

func domainPolicyReadRouters(t *testing.T) (map[string]http.Handler, *auth.JWTService) {
	t.Helper()
	unavailable, failed, _, jwt := authReadBatchRouters(t)
	pool := sql.OpenDB(domainPolicyReadConnection{})
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
	unrestricted := makeRouter()
	previous := heldGrants
	defer func() { heldGrants = previous }()
	scoped := func(domain string) http.Handler {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				return grants.HeldOf(grants.Entry{Capability: middleware.CapDomainsRead, Scope: grants.Domain(domain)}), nil
			}
		}
		return makeRouter()
	}
	return map[string]http.Handler{
		"available": scoped("example.com"), "outside_domain": scoped("other.test"),
		"unauthenticated": unavailable, "request_too_large": failed,
		"invalid_domain": unrestricted, "internal_error": failed, "session_unavailable": unavailable,
	}, jwt
}

func TestDomainPolicyReadExamples(t *testing.T) {
	routers, jwt := domainPolicyReadRouters(t)
	token := mailboxToken(t, jwt)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range domainPolicyReadPaths {
		for name, router := range routers {
			t.Run(operation+"/"+name, func(t *testing.T) {
				pair, ok := pairs[operation][name]
				if !ok {
					t.Fatal("named domain-policy pair missing")
				}
				req := httptest.NewRequest(http.MethodGet, strings.ReplaceAll(path, "{id}", fmt.Sprint(pair.Request.Path["id"])), nil)
				if name != "unauthenticated" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got%d, want%d: %s", rr.Code, pair.Response.Status, rr.Body.String())
				}
				if rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatal("response media type differs")
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, selected := domainPolicyReadPaths[warning.OperationID]; selected {
				t.Errorf("domain-policy list still has warning: %v", warning)
			}
		}
	}
}

func TestDomainPolicyReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for domain-policy boundary replay")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers, jwt := domainPolicyReadRouters(t)
	token := mailboxToken(t, jwt)
	var paths []string
	for _, path := range domainPolicyReadPaths {
		paths = append(paths, path)
	}
	for _, tc := range []struct{ name, status string }{
		{"available", "200"}, {"unauthenticated", "401"}, {"request_too_large", "413"},
		{"outside_domain", "403"},
		{"invalid_domain", "422"}, {"internal_error", "500"}, {"session_unavailable", "503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credential := token
			if tc.name == "unauthenticated" {
				credential = ""
			}
			checkAuthReadWithVertrag(t, binary, routers[tc.name], credential, tc.status, paths)
		})
	}
}
