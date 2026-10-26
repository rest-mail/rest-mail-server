package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
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
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
)

var domainPolicyGetPaths = map[string]string{
	"getDomainRelay":     "/api/v1/admin/domains/{id}/relay",
	"getMtaStsPolicy":    "/api/v1/admin/domains/{id}/mta-sts",
	"deleteMtaStsPolicy": "/api/v1/admin/domains/{id}/mta-sts",
}

var domainPolicyPolicyTable = schema.NamingStrategy{}.TableName("MTASTSPolicy")

func domainPolicyMethod(operation string) string {
	if operation == "deleteMtaStsPolicy" {
		return http.MethodDelete
	}
	return http.MethodGet
}

// GORM still maps real rows and executes its transaction callbacks. The
// controlled IDs select stored, absent and failing domain/settings lookups.
type domainPolicyGetConnection struct{ capabilityExampleConnection }

func (c domainPolicyGetConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c domainPolicyGetConnection) Driver() driver.Driver                        { return c }
func (c domainPolicyGetConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c domainPolicyGetConnection) Begin() (driver.Tx, error) {
	return domainPolicyGetTransaction{}, nil
}

type domainPolicyGetTransaction struct{}

func (domainPolicyGetTransaction) Commit() error   { return nil }
func (domainPolicyGetTransaction) Rollback() error { return nil }

func (c domainPolicyGetConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) == 0 {
		return nil, errors.New("missing domain restriction")
	}
	id := fmt.Sprint(args[0].Value)
	names := map[string]string{"1": "example.com", "2": "direct.test", "3": "unavailable.test", "2147483646": "no-policy.test"}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	switch {
	case strings.Contains(query, `FROM "domains"`):
		if id == "4" {
			return nil, errors.New("sensitive domain database detail")
		}
		if strings.Contains(query, `SELECT "name"`) {
			r := &authReadSnapshotRows{columns: []string{"name"}}
			if name, ok := names[id]; ok {
				r.rows = [][]driver.Value{{name}}
			}
			return r, nil
		}
		r := &authReadSnapshotRows{columns: []string{"id", "name", "server_type", "active", "default_quota_bytes", "dkim_selector", "sending_policy", "dmarc_policy", "created_at", "updated_at"}}
		if name, ok := names[id]; ok {
			var numeric int64
			_, _ = fmt.Sscan(id, &numeric)
			r.rows = [][]driver.Value{{numeric, name, "traditional", true, int64(1073741824), "restmail", "", "none", stamp, stamp}}
		}
		return r, nil
	case strings.Contains(query, `FROM "domain_relays"`):
		if !strings.Contains(query, "domain_id =") {
			return nil, errors.New("relay lookup lost domain restriction")
		}
		if id == "3" {
			return nil, errors.New("sensitive relay database detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "transport", "host", "port", "tls_mode", "username", "password_enc"}}
		if id == "1" {
			r.rows = [][]driver.Value{{int64(1), int64(1), "relay", "smtp.example.net", int64(587), "starttls", "apikey", "stored-test-ciphertext"}}
		}
		return r, nil
	case strings.Contains(query, `FROM "`+domainPolicyPolicyTable+`"`):
		if !strings.Contains(query, "domain_id =") {
			return nil, errors.New("policy lookup lost domain restriction")
		}
		if id == "3" {
			return nil, errors.New("sensitive policy database detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "mode", "mx_hosts", "max_age", "active", "created_at", "updated_at"}}
		if id == "1" {
			r.rows = [][]driver.Value{{int64(7), int64(1), "testing", "mail.example.com", int64(604800), true, stamp, stamp}}
		}
		return r, nil
	}
	return nil, errors.New("unexpected policy query")
}

func (c domainPolicyGetConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, `DELETE FROM "`+domainPolicyPolicyTable+`"`) || !strings.Contains(query, "domain_id =") || len(args) != 1 {
		return nil, errors.New("unexpected policy write")
	}
	switch fmt.Sprint(args[0].Value) {
	case "1":
		return driver.RowsAffected(1), nil
	case "4":
		return nil, errors.New("sensitive policy delete detail")
	default:
		return driver.RowsAffected(0), nil
	}
}

func domainPolicyGetRouters(t *testing.T) (map[string]http.Handler, *auth.JWTService) {
	t.Helper()
	unavailable, failed, _, jwt := authReadBatchRouters(t)
	pool := sql.OpenDB(domainPolicyGetConnection{})
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
	scoped := func(domains ...string) http.Handler {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				var entries []grants.Entry
				for _, domain := range domains {
					for _, capability := range []string{middleware.CapDomainsRead, middleware.CapDomainsWrite} {
						entries = append(entries, grants.Entry{Capability: capability, Scope: grants.Domain(domain)})
					}
				}
				return grants.HeldOf(entries...), nil
			}
		}
		return makeRouter()
	}
	found := scoped("example.com", "direct.test")
	return map[string]http.Handler{
		"found": found, "direct": found, "missing": unrestricted, "policy_absent": unrestricted,
		"unauthenticated": unavailable, "outside_domain": scoped("other.test"),
		"request_too_large": unrestricted, "invalid_domain": unrestricted,
		"internal_error": unrestricted, "settings_unavailable": unrestricted, "session_unavailable": unavailable,
		"domain_store_unavailable": failed,
	}, jwt
}

func TestDomainPolicyDatabaseFailuresAreNotMissingResources(t *testing.T) {
	routers, jwt := domainPolicyGetRouters(t)
	token := mailboxToken(t, jwt)
	writes := NewRouters(newFailingGormDB(t), jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 1024}, nil)
	t.Cleanup(writes.Close)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/admin/domains/1/relay", ""},
		{http.MethodPut, "/api/v1/admin/domains/1/relay", `{"transport":"direct"}`},
		{http.MethodGet, "/api/v1/admin/domains/1/mta-sts", ""},
		{http.MethodDelete, "/api/v1/admin/domains/1/mta-sts", ""},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			handler := routers["domain_store_unavailable"]
			if tc.method == http.MethodPut {
				handler = writes.Public
			}
			handler.ServeHTTP(rr, req)
			if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
				t.Fatalf("database failure: %d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestMtaStsGetReturnsItsQueriedDomain(t *testing.T) {
	routers, jwt := domainPolicyGetRouters(t)
	req := httptest.NewRequest(http.MethodGet, strings.ReplaceAll(domainPolicyGetPaths["getMtaStsPolicy"], "{id}", "1"), nil)
	req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
	rr := httptest.NewRecorder()
	routers["found"].ServeHTTP(rr, req)
	// Decode the public wire keys rather than the database model's fields.
	var body struct {
		Data struct {
			Domain struct {
				ID         uint   `json:"id"`
				Name       string `json:"name"`
				ServerType string `json:"server_type"`
			} `json:"domain"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 200 || body.Data.Domain.ID != 1 || body.Data.Domain.Name != "example.com" || body.Data.Domain.ServerType != "traditional" {
		t.Fatalf("queried domain was not represented: %d %s", rr.Code, rr.Body.String())
	}
}

func TestDomainPolicyGetExamples(t *testing.T) {
	routers, jwt := domainPolicyGetRouters(t)
	token := mailboxToken(t, jwt)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range domainPolicyGetPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				router, ok := routers[name]
				if !ok {
					t.Fatalf("no real precondition for pair %s", name)
				}
				id := fmt.Sprint(pair.Request.Path["id"])
				if numeric, ok := pair.Request.Path["id"].(float64); ok {
					id = strconv.FormatFloat(numeric, 'f', -1, 64)
				}
				req := httptest.NewRequest(domainPolicyMethod(operation), strings.ReplaceAll(path, "{id}", id), nil)
				if name != "unauthenticated" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d, want %d: %s", rr.Code, pair.Response.Status, rr.Body.String())
				}
				if rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatal("response media type differs")
				}
				if pair.Response.Status == 204 {
					if rr.Body.Len() != 0 {
						t.Fatal("204 has a body")
					}
				} else {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, ok := domainPolicyGetPaths[warning.OperationID]; ok {
				t.Errorf("policy operation still has a warning: %v", warning)
			}
		}
	}
}

func TestDomainPolicyGetExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for policy GET/delete replay")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers, jwt := domainPolicyGetRouters(t)
	token := mailboxToken(t, jwt)
	for _, tc := range []struct {
		name, status string
		paths        []string
		count        int
		method       string
	}{
		{"found", "200", []string{domainPolicyGetPaths["getDomainRelay"], domainPolicyGetPaths["getMtaStsPolicy"]}, 3, http.MethodGet},
		{"missing", "404", []string{domainPolicyGetPaths["getDomainRelay"], domainPolicyGetPaths["getMtaStsPolicy"]}, 3, http.MethodGet},
		{"internal_error", "500", []string{domainPolicyGetPaths["getDomainRelay"], domainPolicyGetPaths["getMtaStsPolicy"]}, 4, http.MethodGet},
		// The path type also declares id=1 as its unnamed illustration. It
		// pairs with the bodyless 204 alongside the named found exchange.
		{"found", "204", []string{domainPolicyGetPaths["deleteMtaStsPolicy"]}, 2, http.MethodDelete},
		{"missing", "404", []string{domainPolicyGetPaths["deleteMtaStsPolicy"]}, 1, http.MethodDelete},
		{"internal_error", "500", []string{domainPolicyGetPaths["deleteMtaStsPolicy"]}, 1, http.MethodDelete},
	} {
		t.Run(tc.method+tc.status, func(t *testing.T) {
			checkRegisteredExamplesWithVertrag(t, binary, routers[tc.name], token, tc.method, tc.status, tc.paths, tc.count, false)
		})
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		paths := []string{domainPolicyGetPaths["getDomainRelay"], domainPolicyGetPaths["getMtaStsPolicy"]}
		if method == http.MethodDelete {
			paths = []string{domainPolicyGetPaths["deleteMtaStsPolicy"]}
		}
		for _, tc := range []struct{ name, status string }{{"unauthenticated", "401"}, {"outside_domain", "403"}, {"request_too_large", "413"}, {"invalid_domain", "422"}, {"session_unavailable", "503"}} {
			t.Run(method+tc.name, func(t *testing.T) {
				credential := token
				if tc.name == "unauthenticated" {
					credential = ""
				}
				checkRegisteredExamplesWithVertrag(t, binary, routers[tc.name], credential, method, tc.status, paths, len(paths), tc.status == "413")
			})
		}
	}
}
