package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var publicDiscoveryPaths = map[string]string{
	"restmailDiscover":     "/.well-known/restmail",
	"restmailCheckMailbox": "/restmail/mailboxes",
}

// These fixed rows execute the real repository/normalization and handler wiring.
// They do not stand in for PostgreSQL's own query semantics or the tarpit suite.
type publicDiscoveryConnection struct{ capabilityExampleConnection }

func (c publicDiscoveryConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c publicDiscoveryConnection) Driver() driver.Driver                        { return c }
func (c publicDiscoveryConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c publicDiscoveryConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("public discovery attempted a write")
}
func (c publicDiscoveryConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) == 0 {
		return nil, errors.New("public lookup lost its restriction")
	}
	value := fmt.Sprint(args[0].Value)
	switch {
	case strings.Contains(query, `FROM "domains"`):
		if !strings.Contains(query, "name =") || !strings.Contains(query, "active =") || len(args) < 2 || args[1].Value != true {
			return nil, errors.New("discovery lost active hosted-domain restriction")
		}
		if value == "error.test" {
			return nil, errors.New("private domain query detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id"}}
		if value == "example.test" || value == "xn--mnchen-3ya.test" {
			r.rows = [][]driver.Value{{int64(1)}}
		}
		return r, nil
	case strings.Contains(query, `FROM "mailboxes"`):
		if !strings.Contains(query, "address =") {
			return nil, errors.New("recipient lookup lost address restriction")
		}
		if value == "error@example.test" {
			return nil, errors.New("private recipient query detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "address", "active"}}
		if value == "user@example.test" || value == "disabled@example.test" || value == "café@xn--mnchen-3ya.test" {
			r.rows = [][]driver.Value{{int64(1), value, value != "disabled@example.test"}}
		}
		return r, nil
	}
	return nil, errors.New("unexpected SQL in public discovery")
}

func publicDiscoveryRouters(t *testing.T) map[string]http.Handler {
	t.Helper()
	configureAuthReadPush(t)
	pool := sql.OpenDB(publicDiscoveryConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	makeRouter := func(host string) http.Handler {
		r := NewRouters(db, auth.NewJWTService(routerTestSecret, time.Minute, time.Hour), &config.Config{Environment: "test", GatewayHostname: host}, nil)
		t.Cleanup(r.Close)
		return r.Public
	}
	return map[string]http.Handler{"configured": makeRouter("MX.Example.Test."), "unconfigured": makeRouter("")}
}

func publicDiscoveryURI(path string, pair oapifly.ExamplePair) string {
	q := url.Values{}
	for key, value := range pair.Request.Query {
		if repeated, ok := value.([]interface{}); ok {
			for _, item := range repeated {
				q.Add(key, fmt.Sprint(item))
			}
		} else {
			q.Set(key, fmt.Sprint(value))
		}
	}
	if len(q) > 0 {
		return path + "?" + q.Encode()
	}
	return path
}

func TestPublicDiscoveryDependencyFailureIsNotNegative(t *testing.T) {
	rr := httptest.NewRecorder()
	publicDiscoveryRouters(t)["configured"].ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/restmail/mailboxes?address=error%40example.test", nil))
	if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Failed to look up mailbox\"}}\n" {
		t.Fatalf("dependency failure: got %d %s, want sanitized500", rr.Code, rr.Body.String())
	}
}

func TestPublicDiscoveryExamples(t *testing.T) {
	routers := publicDiscoveryRouters(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range publicDiscoveryPaths {
		if len(pairs[operation]) < 6 {
			t.Fatalf("incomplete public pairs for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, publicDiscoveryURI(path, pair), nil)
				req.Host = "attacker.test"
				req.Header.Set("X-Forwarded-Host", "attacker.test")
				req.Header.Set("Forwarded", "host=attacker.test;proto=http")
				rr := httptest.NewRecorder()
				handler := routers["configured"]
				if name == "unconfigured" {
					handler = routers[name]
				}
				handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
				}
				if operation == "restmailDiscover" && rr.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("discovery answer may be cached")
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, ok := publicDiscoveryPaths[warning.OperationID]; ok {
				t.Errorf("public discovery warning: %v", warning)
			}
		}
	}
}

func TestPublicDiscoveryExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for public discovery exchange proof")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers := publicDiscoveryRouters(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range publicDiscoveryPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				handler := routers["configured"]
				if name == "unconfigured" {
					handler = routers[name]
				}
				checkMailboxLookupWithVertrag(t, binary, handler, "", path, name, pair)
			})
		}
	}
}
