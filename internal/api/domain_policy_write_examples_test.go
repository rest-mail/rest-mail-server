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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/api/handlers"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/grants"
)

var domainPolicyWritePaths = map[string]string{
	"setDomainRelay":  "/api/v1/admin/domains/{id}/relay",
	"setMtaStsPolicy": "/api/v1/admin/domains/{id}/mta-sts",
}

var domainPolicyWriteTime = time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)

type domainPolicyRecordedWrite struct {
	table  string
	fields map[string]driver.Value
}

// Record real GORM write arguments, without pretending to prove PostgreSQL
// semantics. Reads use the existing configured/absent/failing domain fixtures.
type domainPolicyWriteConnection struct {
	domainPolicyGetConnection
	mu         sync.Mutex
	writes     []domainPolicyRecordedWrite
	failWrites bool
}

func (c *domainPolicyWriteConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *domainPolicyWriteConnection) Driver() driver.Driver                        { return c }
func (c *domainPolicyWriteConnection) Open(string) (driver.Conn, error)             { return c, nil }

var domainPolicyInsertColumns = regexp.MustCompile(`^INSERT INTO "([^"]+)" \(([^)]+)\) VALUES`)
var domainPolicyUpdateColumns = regexp.MustCompile(`"([^"]+)"=\$(\d+)`)
var domainPolicyUpdateDomain = regexp.MustCompile(` WHERE domain_id = \$(\d+)`)

func (c *domainPolicyWriteConnection) record(query string, args []driver.NamedValue) error {
	if c.failWrites {
		return errors.New("controlled policy write unavailable")
	}
	write := domainPolicyRecordedWrite{fields: map[string]driver.Value{}}
	if parts := domainPolicyInsertColumns.FindStringSubmatch(query); parts != nil {
		write.table = parts[1]
		columns := strings.Split(parts[2], ",")
		if len(columns) != len(args) {
			return errors.New("unexpected policy insert arguments")
		}
		for i, col := range columns {
			write.fields[strings.Trim(col, `"`)] = args[i].Value
		}
	} else if strings.HasPrefix(query, "UPDATE ") {
		parts := strings.Split(query, `"`)
		write.table = parts[1]
		// Stale-safe updates also bind the domain: WHERE domain_id = $n AND "id" = $m.
		if !strings.Contains(query, " WHERE ") || !strings.Contains(query, `"id" = $`) {
			return errors.New("unrestricted policy update")
		}
		for _, match := range domainPolicyUpdateColumns.FindAllStringSubmatch(query, -1) {
			index, _ := strconv.Atoi(match[2])
			write.fields[match[1]] = args[index-1].Value
		}
		if match := domainPolicyUpdateDomain.FindStringSubmatch(query); match != nil {
			index, _ := strconv.Atoi(match[1])
			write.fields["domain_id"] = args[index-1].Value
		}
	} else {
		return errors.New("unexpected policy SQL write")
	}
	if write.table != "domain_relays" && write.table != domainPolicyPolicyTable {
		return errors.New("unrelated SQL write")
	}
	c.mu.Lock()
	c.writes = append(c.writes, write)
	c.mu.Unlock()
	return nil
}

func (c *domainPolicyWriteConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(query, "INSERT ") {
		if err := c.record(query, args); err != nil {
			return nil, err
		}
		return &authReadSnapshotRows{columns: []string{"id"}, rows: [][]driver.Value{{int64(8)}}}, nil
	}
	if strings.Contains(query, `FROM "domains"`) && len(args) > 0 && fmt.Sprint(args[0].Value) == "5" {
		return &authReadSnapshotRows{columns: []string{"id", "name", "server_type", "active", "default_quota_bytes", "dkim_selector", "sending_policy", "dmarc_policy", "created_at", "updated_at"}, rows: [][]driver.Value{{int64(5), "save.test", "traditional", true, int64(1073741824), "restmail", "", "none", domainPolicyWriteTime, domainPolicyWriteTime}}}, nil
	}
	return c.domainPolicyGetConnection.QueryContext(ctx, query, args)
}

func (c *domainPolicyWriteConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.record(query, args); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func domainPolicyWriteRouters(t *testing.T) (map[string]http.Handler, *auth.JWTService, map[string]*domainPolicyWriteConnection) {
	t.Helper()
	configureAuthReadPush(t)
	useRealGrants(t)
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	makeRouter := func(db *gorm.DB, cap int64, key string) http.Handler {
		r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: cap, MasterKey: key}, nil)
		t.Cleanup(r.Close)
		return r.Public
	}
	unavailable := makeRouter(newFailingGormDB(t), 4096, accessMasterKey)
	sessionCheck = func(*handlers.SessionCache) middleware.SessionCurrent {
		return func(uint, int) (bool, error) { return true, nil }
	}
	heldGrants = func(*grants.Cache) middleware.HeldGrants {
		return func(uint) (grants.Held, error) {
			return grants.HeldOf(grants.Entry{Capability: grants.Wildcard, Scope: grants.All}), nil
		}
	}
	routers := map[string]http.Handler{"session_unavailable": unavailable, "unauthenticated": unavailable}
	stores := map[string]*domainPolicyWriteConnection{}
	for _, name := range []string{"normal", "master_key_required", "save_unavailable", "outside_domain", "domain_grant", "request_too_large"} {
		store := &domainPolicyWriteConnection{failWrites: name == "save_unavailable"}
		pool := sql.OpenDB(store)
		t.Cleanup(func() { _ = pool.Close() })
		db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent), NowFunc: func() time.Time { return domainPolicyWriteTime }})
		if err != nil {
			t.Fatal(err)
		}
		key, cap := accessMasterKey, int64(4096)
		if name == "master_key_required" {
			key = ""
		}
		if name == "request_too_large" {
			cap = 8
		}
		previous := heldGrants
		if name == "outside_domain" || name == "domain_grant" {
			domain := "other.test"
			if name == "domain_grant" {
				domain = "example.com"
			}
			heldGrants = func(*grants.Cache) middleware.HeldGrants {
				return func(uint) (grants.Held, error) {
					return grants.HeldOf(grants.Entry{Capability: middleware.CapDomainsWrite, Scope: grants.Domain(domain)}), nil
				}
			}
		}
		routers[name] = makeRouter(db, cap, key)
		heldGrants = previous
		stores[name] = store
	}
	routers["rate_limited"] = apiKeyRefusalLimited(t)
	return routers, jwt, stores
}

func TestDomainPolicyWriteScopeBoundaries(t *testing.T) {
	routers, jwt, stores := domainPolicyWriteRouters(t)
	for _, tc := range []struct {
		operation, body string
		status          int
	}{
		{"setMtaStsPolicy", `{"mode":"testing"}`, 200},
		// Relay selection requires the existing installation-wide ceiling.
		{"setDomainRelay", `{"transport":"direct"}`, 403},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, strings.ReplaceAll(domainPolicyWritePaths[tc.operation], "{id}", "1"), strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			routers["domain_grant"].ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("domain-scoped write: got %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
		})
	}
	store := stores["domain_grant"]
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.writes) != 1 || store.writes[0].table != domainPolicyPolicyTable {
		t.Fatal("domain-scoped grant must only write its MTA-STS policy")
	}
}

func TestMtaStsWritePreservesDatabaseFailuresAndDomain(t *testing.T) {
	routers, jwt, _ := domainPolicyWriteRouters(t)
	for _, tc := range []struct {
		id     string
		status int
	}{{"4", 500}, {"1", 200}, {"2", 201}} {
		t.Run(tc.id, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, strings.ReplaceAll(domainPolicyWritePaths["setMtaStsPolicy"], "{id}", tc.id), strings.NewReader(`{"mode":"testing"}`))
			req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			routers["normal"].ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if tc.status == 500 {
				if rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
					t.Fatal(rr.Body.String())
				}
			} else {
				var body struct {
					Data struct {
						Domain struct {
							ID uint `json:"id"`
						} `json:"domain"`
					} `json:"data"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				id, _ := strconv.Atoi(tc.id)
				if body.Data.Domain.ID != uint(id) {
					t.Fatalf("missing loaded domain: %s", rr.Body.String())
				}
			}
		})
	}
}

func domainPolicyWritePrecondition(name string) string {
	switch name {
	case "unauthenticated", "session_unavailable", "outside_domain", "request_too_large", "master_key_required", "save_unavailable", "rate_limited":
		return name
	default:
		return "normal"
	}
}

func TestDomainPolicyWriteExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range domainPolicyWritePaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				routers, jwt, stores := domainPolicyWriteRouters(t)
				precondition := domainPolicyWritePrecondition(name)
				id := "1"
				if value, ok := pair.Request.Path["id"]; ok {
					id = fmt.Sprint(value)
					if number, ok := value.(float64); ok {
						id = strconv.FormatFloat(number, 'f', -1, 64)
					}
				}
				req := httptest.NewRequest(http.MethodPut, strings.ReplaceAll(path, "{id}", id), strings.NewReader(string(pair.Request.Body)))
				req.Header.Set("Content-Type", "application/json")
				if name != "unauthenticated" {
					req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
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
					t.Fatal("wrong media type")
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				store := stores[precondition]
				if store == nil {
					return
				}
				store.mu.Lock()
				defer store.mu.Unlock()
				if rr.Code >= 400 {
					if len(store.writes) != 0 {
						t.Fatal("refused request wrote policy data")
					}
					return
				}
				if len(store.writes) != 1 {
					t.Fatalf("want exactly one policy write, got %d", len(store.writes))
				}
				fields := store.writes[0].fields
				var expected struct {
					Data map[string]interface{} `json:"data"`
				}
				if err := json.Unmarshal(pair.Response.Body, &expected); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"domain_id", "transport", "host", "port", "tls_mode", "username", "mode", "mx_hosts", "max_age", "active"} {
					if want, ok := expected.Data[key]; ok && fmt.Sprint(fields[key]) != fmt.Sprint(want) {
						t.Errorf("stored %s=%v, response=%v", key, fields[key], want)
					}
				}
				if operation == "setDomainRelay" {
					cipher, _ := fields["password_enc"].(string)
					switch name {
					case "relay":
						plain, err := models.LoadRelayPassword(cipher, accessMasterKey)
						if err != nil || plain != "relay-password" || cipher == plain {
							t.Fatal("relay password was not encrypted at the persistence boundary")
						}
					case "direct":
						for _, key := range []string{"host", "tls_mode", "username", "password_enc"} {
							if fields[key] != "" {
								t.Errorf("direct retained %s", key)
							}
						}
						if fmt.Sprint(fields["port"]) != "0" {
							t.Fatal("direct retained relay port")
						}
					case "credentials_retained":
						if cipher != "stored-test-ciphertext" {
							t.Fatal("omitted password changed stored credential")
						}
					}
				}
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, ok := domainPolicyWritePaths[warning.OperationID]; ok {
				t.Errorf("write operation still has warning: %v", warning)
			}
		}
	}
}

func TestDomainPolicyWriteExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for policy write replay")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range domainPolicyWritePaths {
		counts := map[string]int{}
		for _, pair := range pairs[operation] {
			counts[fmt.Sprint(pair.Response.Status)]++
		}
		for status, count := range counts {
			t.Run(operation+"/"+status, func(t *testing.T) {
				routers, jwt, _ := domainPolicyWriteRouters(t)
				token := mailboxToken(t, jwt)
				precondition := map[string]string{"401": "unauthenticated", "403": "outside_domain", "409": "master_key_required", "413": "request_too_large", "503": "session_unavailable"}[status]
				if precondition == "" {
					precondition = "normal"
				}
				if status == "401" {
					token = ""
				}
				// Each status has one precondition except500: route by published
				// domain ID so domain, lookup and save faults remain separate.
				handler := routers[precondition]
				if status == "500" {
					handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if strings.Contains(r.URL.Path, "/5/") {
							routers["save_unavailable"].ServeHTTP(w, r)
						} else {
							routers["normal"].ServeHTTP(w, r)
						}
					})
				}
				checkRegisteredExamplesWithVertrag(t, binary, handler, token, http.MethodPut, status, []string{path}, count, false)
			})
		}
	}
}
