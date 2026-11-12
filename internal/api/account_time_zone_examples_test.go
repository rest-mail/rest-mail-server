package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

func accountTimeZoneExamples(t *testing.T) {
	t.Helper()
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	f := newCredentialFixture(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/accounts/{id}/time-zone"
	for _, operation := range []string{"getAccountTimeZone", "setAccountTimeZone"} {
		method := http.MethodGet
		capability := models.CapMailWrite
		if operation == "setAccountTimeZone" {
			method = http.MethodPut
			capability = models.CapMailRead
		}
		key, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		credential := models.Credential{AccountID: f.accountID, Kind: models.CredentialKindAPIKey, Name: operation, PublicID: public, SecretHash: hash,
			Scope: models.CredentialScope{Permissions: []auth.ScopedPermission{{Capability: capability, Scope: grants.Mailbox(f.address)}}}}
		if err := f.db.Create(&credential).Error; err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(pairs[operation]))
		for name := range pairs[operation] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			pair := pairs[operation][name]
			t.Run(operation+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, operation, name)
				if err := f.db.Model(&models.Mailbox{}).Where("id = ?", f.mailboxID).Update("time_zone", nil).Error; err != nil {
					t.Fatal(err)
				}
				cfg := &config.Config{Environment: "test", APIMaxBodyBytes: 256}
				if name == "rate_limited" {
					cfg.AuthRateLimitEnabled = true
					cfg.AuthRateLimitRPS = 1.0 / 3600
					cfg.AuthRateLimitBurst = 1
				}
				routers := NewRouters(f.db, f.jwt, cfg, nil)
				t.Cleanup(routers.Close)
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r.RemoteAddr = "192.0.2.1:12345"
					routers.Public.ServeHTTP(w, r)
				})
				uri := strings.ReplaceAll(path, "{id}", fmt.Sprint(f.mailboxID))
				if name == "invalid_id" || name == "missing" {
					uri = strings.ReplaceAll(path, "{id}", fmt.Sprintf("%.0f", pair.Request.Path["id"]))
				}
				token := f.session
				if name == "unauthenticated" {
					token = ""
				}
				if name == "without_capability" {
					token = "Bearer " + key
				}
				if name == "rate_limited" {
					token = "Bearer rmk_invalid"
				}
				body := string(pair.Request.Body)
				if name == "malformed_json" {
					body = "{"
				}
				if name == "request_too_large" {
					body = strings.Repeat("x", 257)
				}
				if name == "internal_error" || name == "session_unavailable" {
					reads := 0
					const callback = "account_time_zone_example_fault"
					if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
						if query.Statement.Table == "mailboxes" {
							reads++
							if name == "session_unavailable" || reads > 1 {
								_ = query.AddError(errors.New("private time zone storage fault"))
							}
						}
					}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
				}
				send := func() *httptest.ResponseRecorder {
					rr := httptest.NewRecorder()
					req := httptest.NewRequest(method, uri, strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					if token != "" {
						req.Header.Set("Authorization", token)
					}
					handler.ServeHTTP(rr, req)
					return rr
				}
				if name == "rate_limited" {
					send()
				}
				rr := send()
				if rr.Code != pair.Response.Status {
					t.Fatalf("want %d: %d %s", pair.Response.Status, rr.Code, rr.Body)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
}

func TestAccountTimeZoneExamples(t *testing.T) { accountTimeZoneExamples(t) }

func TestAccountTimeZoneWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for time zone exchange proof")
	}
	accountTimeZoneContractExamples(t, binary)
}

func TestAccountTimeZoneCoverageWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for time-zone coverage proof")
	}
	fixture := newCredentialFixture(t)
	document := contractDocument(t, describedRouters(t).pairGenerator(t, "time-zone coverage"))
	path := "/api/v1/accounts/{id}/time-zone"
	operations := document["paths"].(map[string]any)[path].(map[string]any)
	for _, method := range []string{"get", "put"} {
		operation := operations[method].(map[string]any)
		responses := operation["responses"].(map[string]any)
		operation["responses"] = map[string]any{"200": responses["200"]}
		if body, ok := operation["requestBody"].(map[string]any); ok {
			for _, content := range body["content"].(map[string]any) {
				media := content.(map[string]any)
				examples := media["examples"].(map[string]any)
				media["examples"] = map[string]any{"set": examples["set"], "cleared": examples["cleared"]}
			}
		}
	}
	document["paths"] = map[string]any{path: operations}
	directory := t.TempDir()
	data, err := marshalPairDescription(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "openapi.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	hooks, err := filepath.Abs("../../tests/contract/quiet_hours_hooks.py")
	if err != nil {
		t.Fatal(err)
	}
	hook := fmt.Sprintf("import runpy\nloaded = runpy.run_path(%q)\nloaded['register_quiet_hours_hooks'](lambda _: %q, 'time-zone', ('getAccountTimeZone', 'setAccountTimeZone'))\n", hooks, strings.TrimPrefix(fixture.session, "Bearer "))
	if err := os.WriteFile(filepath.Join(directory, "time_zone.py"), []byte(hook), 0600); err != nil {
		t.Fatal(err)
	}
	// This run has its own configuration, so it holds time_zone as probes.py holds it
	// for the whole API: the schema cannot say which strings are IANA zones, so the
	// generator's x reaches a server that rightly refuses it. invalid_zone proves that.
	if err := os.WriteFile(filepath.Join(directory, "vertrag.yml"), []byte("language: python\nhookfiles: ./time_zone.py\nfuzz:\n  pin:\n    time_zone: UTC\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fixture.router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "run", "--endpoint", server.URL, "--phases", "examples,coverage", "--fail-on-skipped-probes", "--header", "Authorization: "+fixture.session, "--reporter", "json", "--output", "report.json", "openapi.json")
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("time-zone coverage failed: %v\n%s", err, output)
	}
	data, err = os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Summary struct{ Total, Passing, Failing, Errors, Skipped int }
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.Total <= 3 || report.Summary.Failing != 0 || report.Summary.Errors != 0 || report.Summary.Skipped != 0 {
		t.Fatalf("coverage did not execute without skips: %s", data)
	}
}

type accountTimeZoneExampleConnection struct{ mailRemovalConnection }

func (connection accountTimeZoneExampleConnection) Connect(context.Context) (driver.Conn, error) {
	return connection, nil
}

func (connection accountTimeZoneExampleConnection) Driver() driver.Driver { return connection }

func (connection accountTimeZoneExampleConnection) Open(string) (driver.Conn, error) {
	return connection, nil
}

func (connection accountTimeZoneExampleConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, `FROM "mailboxes"`) && len(args) != 0 && args[0].Value == int64(3) {
		return nil, errors.New("private time zone lookup fault")
	}
	return connection.mailRemovalConnection.QueryContext(ctx, query, args)
}

func (connection accountTimeZoneExampleConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, `UPDATE "mailboxes" SET`) {
		return nil, errors.New("unexpected time zone write")
	}
	fields, err := sieveInstallationFields(query, args)
	if err != nil {
		return nil, err
	}
	if fields["id"] != int64(1) || len(fields) != 3 {
		return nil, errors.New("time zone write lost its account or wrote unrelated fields")
	}
	if _, ok := fields["time_zone"]; !ok {
		return nil, errors.New("time zone write did not name the column")
	}
	connection.record(query, args)
	return driver.RowsAffected(1), nil
}

func accountTimeZoneContractExamples(t *testing.T, binary string) {
	t.Helper()
	fixture := newMailRulesWriteScenarios(t, func(base mailRemovalConnection) driver.Connector {
		return accountTimeZoneExampleConnection{base}
	})
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/accounts/{id}/time-zone"
	for _, operation := range []string{"getAccountTimeZone", "setAccountTimeZone"} {
		method := http.MethodGet
		if operation == "setAccountTimeZone" {
			method = http.MethodPut
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, operation, name)
				scenario := fixture.scenarios["found"]
				if name == "without_capability" {
					scenario = fixture.scenarios["key_write_only"]
					if method == http.MethodPut {
						scenario = fixture.scenarios["key_read"]
					}
				}
				if name == "unauthenticated" || name == "session_unavailable" || name == "rate_limited" {
					scenario = fixture.scenarios[name]
				}
				body := string(pair.Request.Body)
				if name == "malformed_json" {
					body = "{"
				}
				if name == "request_too_large" {
					body = strings.Repeat("x", 257)
				}
				uri := domainAliasReadURI(path, pair)
				checkNamedPairWithVertrag(t, binary, scenario.handler, namedPairExchange{method: method, path: path, uri: uri, name: name, status: pair.Response.Status, body: body, token: scenario.token})
			})
		}
	}
}
