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
	"sync"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/grants"
)

var domainPolicyDeletePaths = map[string]string{
	"removeFromAllowlist": "/api/v1/admin/domains/{id}/allowlist/{eid}",
	"removeFromBlocklist": "/api/v1/admin/domains/{id}/blocklist/{eid}",
	"clearSuppression":    "/api/v1/admin/domains/{id}/suppressions/{sid}",
}

type domainPolicyDeleteConnection struct {
	domainPolicyGetConnection
	listType string
	mu       sync.Mutex
	loaded   string
	deleted  []string
}

func (c *domainPolicyDeleteConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *domainPolicyDeleteConnection) Driver() driver.Driver                        { return c }
func (c *domainPolicyDeleteConnection) Open(string) (driver.Conn, error)             { return c, nil }

func (c *domainPolicyDeleteConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, `FROM "domain_sender_rules"`) {
		return c.domainPolicyGetConnection.QueryContext(ctx, query, args)
	}
	if !strings.Contains(query, "id =") || !strings.Contains(query, "domain_id =") || !strings.Contains(query, "list_type =") || len(args) < 3 || args[2].Value != c.listType {
		return nil, errors.New("sender lookup lost its entry/domain/list restriction")
	}
	id, domain := fmt.Sprint(args[0].Value), fmt.Sprint(args[1].Value)
	if id == "3" {
		return nil, errors.New("sensitive sender lookup detail")
	}
	rows := &authReadSnapshotRows{columns: []string{"id", "domain_id", "list_type"}}
	// Entry2 belongs to the opposite list; entry1 belongs to domain1 only.
	if domain == "1" && (id == "1" || id == "4") {
		numeric, _ := strconv.ParseInt(id, 10, 64)
		rows.rows = [][]driver.Value{{numeric, int64(1), c.listType}}
		c.mu.Lock()
		c.loaded = id
		c.mu.Unlock()
	}
	return rows, nil
}

func (c *domainPolicyDeleteConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.HasPrefix(query, `DELETE FROM "domain_sender_rules"`) {
		if len(args) != 3 || fmt.Sprint(args[0].Value) != c.loaded || !strings.Contains(query, "WHERE id =") || !strings.Contains(query, "domain_id =") || !strings.Contains(query, "list_type =") || args[2].Value != c.listType {
			return nil, errors.New("delete did not use its previously scoped row")
		}
		if c.loaded == "4" {
			return nil, errors.New("sensitive sender delete detail")
		}
		c.deleted = append(c.deleted, c.loaded)
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, `DELETE FROM "suppressions"`) {
		if len(args) != 2 || !strings.Contains(query, "id =") || !strings.Contains(query, "domain_id =") {
			return nil, errors.New("suppression delete lost entry/domain restriction")
		}
		id, domain := fmt.Sprint(args[0].Value), fmt.Sprint(args[1].Value)
		if id == "3" || id == "4" {
			return nil, errors.New("sensitive suppression delete detail")
		}
		if id == "1" && domain == "1" {
			c.deleted = append(c.deleted, id)
			return driver.RowsAffected(1), nil
		}
		return driver.RowsAffected(0), nil
	}
	return nil, errors.New("unrelated SQL write in deletion fixture")
}

func domainPolicyDeleteRouters(t *testing.T, operation string) (map[string]http.Handler, *auth.JWTService, *domainPolicyDeleteConnection) {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	store := &domainPolicyDeleteConnection{listType: "allow"}
	if operation == "removeFromBlocklist" {
		store.listType = "block"
	}
	pool := sql.OpenDB(store)
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
	normal := makeRouter()
	scoped := func(domain string) http.Handler {
		old := heldGrants
		defer func() { heldGrants = old }()
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				return grants.HeldOf(grants.Entry{Capability: middleware.CapDomainsWrite, Scope: grants.Domain(domain)}), nil
			}
		}
		return makeRouter()
	}
	return map[string]http.Handler{"rate_limited": apiKeyRefusalLimited(t), "normal": normal, "found": scoped("example.com"), "outside_domain": scoped("other.test"), "unauthenticated": unavailable, "session_unavailable": unavailable}, jwt, store
}

func TestSenderRuleDeleteLookupFailuresAreNotMissingRows(t *testing.T) {
	for _, operation := range []string{"removeFromAllowlist", "removeFromBlocklist"} {
		t.Run(operation, func(t *testing.T) {
			routers, jwt, _ := domainPolicyDeleteRouters(t, operation)
			path := strings.NewReplacer("{id}", "1", "{eid}", "3").Replace(domainPolicyDeletePaths[operation])
			req := httptest.NewRequest(http.MethodDelete, path, nil)
			req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
			rr := httptest.NewRecorder()
			routers["normal"].ServeHTTP(rr, req)
			if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
				t.Fatalf("query failure: %d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestDomainPolicyDeleteExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range domainPolicyDeletePaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				routers, jwt, store := domainPolicyDeleteRouters(t, operation)
				requestPath := path
				for key, value := range pair.Request.Path {
					text := fmt.Sprint(value)
					if number, ok := value.(float64); ok {
						text = strconv.FormatFloat(number, 'f', -1, 64)
					}
					requestPath = strings.ReplaceAll(requestPath, "{"+key+"}", text)
				}
				req := httptest.NewRequest(http.MethodDelete, requestPath, nil)
				if name != "unauthenticated" {
					req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
				}
				if name == "request_too_large" {
					req.ContentLength = 9
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
				if rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatal("response media type differs")
				}
				if rr.Code == 204 {
					if rr.Body.Len() != 0 {
						t.Fatal("204 has a body")
					}
				} else {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				}
				store.mu.Lock()
				defer store.mu.Unlock()
				want := 0
				if rr.Code == 204 {
					want = 1
				}
				if len(store.deleted) != want {
					t.Fatalf("deleted %d rows, want %d", len(store.deleted), want)
				}
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, ok := domainPolicyDeletePaths[warning.OperationID]; ok {
				t.Errorf("delete operation still has warning: %v", warning)
			}
		}
	}
}

func TestDomainPolicyDeleteExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for policy delete replay")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range domainPolicyDeletePaths {
		counts := map[string]int{}
		for _, pair := range pairs[operation] {
			counts[fmt.Sprint(pair.Response.Status)]++
		}
		for status, count := range counts {
			t.Run(operation+"/"+status, func(t *testing.T) {
				routers, jwt, _ := domainPolicyDeleteRouters(t, operation)
				token := mailboxToken(t, jwt)
				precondition := map[string]string{"204": "found", "401": "unauthenticated", "403": "outside_domain", "503": "session_unavailable"}[status]
				if precondition == "" {
					precondition = "normal"
				}
				if status == "401" {
					token = ""
				}
				// Bodyless204 also pairs with the path type's unnamed id/eid/sid=1 illustration.
				if status == "204" {
					count++
				}
				checkRegisteredExamplesWithVertrag(t, binary, routers[precondition], token, http.MethodDelete, status, []string{path}, count, status == "413")
			})
		}
	}
}
