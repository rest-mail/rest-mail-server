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
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail"
	"github.com/restmail/restmail/internal/config"
)

const policyFilePath = "/.well-known/mta-sts.txt"

type policyFileConnection struct{ domainPolicyGetConnection }

func (c policyFileConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c policyFileConnection) Driver() driver.Driver                        { return c }
func (c policyFileConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c policyFileConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, `FROM "domains"`) {
		if !strings.Contains(query, "name IN") {
			return nil, errors.New("policy host lookup lost its domain names")
		}
		ids := map[string]int64{"example.com": 1, "xn--bcher-kva.example": 2, "bücher.example": 2, "no-policy.test": 3, "policy-failure.test": 4, "invalid-policy.test": 5, "inactive.test": 6}
		rows := &authReadSnapshotRows{columns: []string{"id", "name"}}
		for _, arg := range args {
			if name, ok := arg.Value.(string); ok {
				if name == "domain-failure.test" {
					return nil, errors.New("sensitive policy domain detail")
				}
				if id, ok := ids[name]; ok {
					rows.rows = [][]driver.Value{{id, name}}
					break
				}
			}
		}
		return rows, nil
	}
	if strings.Contains(query, `FROM "`+domainPolicyPolicyTable+`"`) {
		if !strings.Contains(query, "domain_id =") || !strings.Contains(query, "active =") || len(args) < 2 || args[1].Value != true {
			return nil, errors.New("policy lookup lost its domain or active restriction")
		}
		id := fmt.Sprint(args[0].Value)
		if id == "4" {
			return nil, errors.New("sensitive active policy detail")
		}
		rows := &authReadSnapshotRows{columns: []string{"id", "domain_id", "mode", "mx_hosts", "max_age", "active"}}
		if id == "1" || id == "2" || id == "5" {
			mx := "mail.example.com"
			if id == "2" {
				mx = "mail.bücher.example"
			}
			if id == "5" {
				mx = "mail.example.com\nmode: none"
			}
			rows.rows = [][]driver.Value{{int64(1), args[0].Value, "enforce", mx, int64(604800), true}}
		}
		return rows, nil
	}
	return nil, errors.New("unexpected public policy query")
}

func policyFileRouter(t *testing.T) http.Handler {
	t.Helper()
	pool := sql.OpenDB(policyFileConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	_, jwt := newTestRouter(t)
	routers := NewRouters(db, jwt, &config.Config{Environment: "test"}, nil)
	t.Cleanup(routers.Close)
	return routers.Public
}

func TestPolicyFileLookupFailuresAreNotMissingPolicies(t *testing.T) {
	router := policyFileRouter(t)
	for _, host := range []string{"mta-sts.domain-failure.test", "mta-sts.policy-failure.test"} {
		t.Run(host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, policyFilePath, nil)
			req.Host = host
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
				t.Fatalf("policy lookup failure: %d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestMtaStsPolicyFileExamples(t *testing.T) {
	router := policyFileRouter(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["mtaStsPolicyFile"] {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, policyFilePath, nil)
			req.Host = fmt.Sprint(pair.Request.Header["Host"])
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got %d,want %d: %s", rr.Code, pair.Response.Status, rr.Body.String())
			}
			contentType := pair.Response.MediaType
			if rr.Code == 200 {
				contentType += "; charset=utf-8"
			}
			if rr.Header().Get("Content-Type") != contentType {
				t.Fatalf("media type %q,want %q", rr.Header().Get("Content-Type"), contentType)
			}
			if rr.Code == 200 {
				var body string
				if err := json.Unmarshal(pair.Response.Body, &body); err != nil {
					t.Fatal(err)
				}
				if rr.Body.String() != body {
					t.Fatalf("policy bytes %q,want %q", rr.Body.String(), body)
				}
			} else {
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			}
		})
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if warning.OperationID == "mtaStsPolicyFile" {
				t.Errorf("policy file still has warning: %v", warning)
			}
		}
	}
}

func TestMtaStsPolicyFileExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for public policy replay")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, pair := range pairs["mtaStsPolicyFile"] {
		counts[fmt.Sprint(pair.Response.Status)]++
	}
	for status, count := range counts {
		t.Run(status, func(t *testing.T) {
			checkRegisteredExamplesWithVertrag(t, binary, policyFileRouter(t), "", http.MethodGet, status, []string{policyFilePath}, count, false)
		})
	}
}
