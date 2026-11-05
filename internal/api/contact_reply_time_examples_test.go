package api

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

func contactReplyTimeExamples(t *testing.T) {
	t.Helper()
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	f := newCredentialFixture(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/accounts/{id}/contact-reply-time"
	for _, operation := range []string{"getContactReplyTimeSetting", "setContactReplyTimeSetting"} {
		method := http.MethodGet
		capability := models.CapMailWrite
		if operation == "setContactReplyTimeSetting" {
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
				if err := f.db.Model(&models.Mailbox{}).Where("id = ?", f.mailboxID).Update("contact_reply_time_enabled", false).Error; err != nil {
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
					const callback = "contact_reply_time_example_fault"
					if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(query *gorm.DB) {
						if query.Statement.Table == "mailboxes" {
							reads++
							if name == "session_unavailable" || reads > 1 {
								_ = query.AddError(errors.New("private reply-time storage fault"))
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

func TestContactReplyTimeExamples(t *testing.T) { contactReplyTimeExamples(t) }

func TestContactReplyTimeExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for contact reply-time exchange proof")
	}
	contactReplyTimeContractExamples(t, binary)
}

type contactReplyTimeExampleConnection struct{ mailRemovalConnection }

func (connection contactReplyTimeExampleConnection) Connect(context.Context) (driver.Conn, error) {
	return connection, nil
}

func (connection contactReplyTimeExampleConnection) Driver() driver.Driver { return connection }

func (connection contactReplyTimeExampleConnection) Open(string) (driver.Conn, error) {
	return connection, nil
}

func (connection contactReplyTimeExampleConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, `FROM "mailboxes"`) && len(args) != 0 && args[0].Value == int64(3) {
		return nil, errors.New("private reply-time lookup fault")
	}
	return connection.mailRemovalConnection.QueryContext(ctx, query, args)
}

func (connection contactReplyTimeExampleConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(query, `UPDATE "mailboxes" SET`) {
		return nil, errors.New("unexpected reply-time setting write")
	}
	fields, err := sieveInstallationFields(query, args)
	if err != nil {
		return nil, err
	}
	if fields["id"] != int64(1) || len(fields) != 3 {
		return nil, errors.New("reply-time setting lost account selector or wrote unrelated fields")
	}
	if _, ok := fields["contact_reply_time_enabled"].(bool); !ok {
		return nil, errors.New("reply-time setting lost boolean value")
	}
	connection.record(query, args)
	return driver.RowsAffected(1), nil
}

func contactReplyTimeContractExamples(t *testing.T, binary string) {
	t.Helper()
	fixture := newMailRulesWriteScenarios(t, func(base mailRemovalConnection) driver.Connector {
		return contactReplyTimeExampleConnection{base}
	})
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/accounts/{id}/contact-reply-time"
	for _, operation := range []string{"getContactReplyTimeSetting", "setContactReplyTimeSetting"} {
		method := http.MethodGet
		if operation == "setContactReplyTimeSetting" {
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
