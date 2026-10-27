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
	"net/url"
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

var mailboxLookupPaths = map[string]string{
	"checkMailboxAddress": "/api/mailboxes",
	"getMailbox":          "/api/v1/admin/mailboxes/{id}",
}

// Real repository queries and GORM association loading run over controlled SQL
// rows. This tests HTTP wiring without claiming PostgreSQL query semantics.
type mailboxLookupConnection struct{ capabilityExampleConnection }

func (c mailboxLookupConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c mailboxLookupConnection) Driver() driver.Driver                        { return c }
func (c mailboxLookupConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c mailboxLookupConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(args) == 0 {
		return nil, errors.New("lookup lost its identity restriction")
	}
	id := fmt.Sprint(args[0].Value)
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	switch {
	case strings.Contains(query, `FROM "mailboxes"`):
		if strings.Contains(query, "address =") {
			switch id {
			case "user@example.com":
				id = "1"
			case "disabled@example.com":
				id = "2"
			case "unavailable@example.com":
				return nil, errors.New("sensitive recipient query failure")
			default:
				id = "2147483647"
			}
		} else if !strings.Contains(query, `"mailboxes"."id" =`) && !strings.Contains(query, "id =") {
			return nil, errors.New("mailbox query lost ID restriction")
		}
		if strings.Contains(query, `SELECT "address"`) {
			if id == "8" {
				return nil, errors.New("sensitive permission lookup failure")
			}
			r := &authReadSnapshotRows{columns: []string{"address"}}
			if id != "2147483647" {
				r.rows = [][]driver.Value{{"user@example.com"}}
			}
			return r, nil
		}
		if id == "3" {
			return nil, errors.New("sensitive mailbox query failure")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "local_part", "address", "display_name", "quota_bytes", "quota_used_bytes", "active", "last_login_at", "password_change_required", "two_factor_required", "credential_epoch", "bootstrap_expires_at", "last_password_change", "created_at", "updated_at"}}
		if id != "2147483647" {
			numeric, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return nil, err
			}
			local, domainID := "user", int64(1)
			if id == "2" {
				local = "disabled"
			}
			if id == "4" {
				domainID = 2
			}
			r.rows = [][]driver.Value{{numeric, domainID, local, local + "@example.com", "Mailbox café", int64(1048576), int64(321), id != "2", stamp, false, true, int64(17), stamp, stamp, stamp, stamp}}
		}
		return r, nil
	case strings.Contains(query, `FROM "domains"`):
		if id == "2" {
			return nil, errors.New("sensitive domain preload failure")
		}
		return &authReadSnapshotRows{columns: []string{"id", "name", "server_type", "active", "default_quota_bytes", "dkim_selector", "sending_policy", "dmarc_policy", "created_at", "updated_at"}, rows: [][]driver.Value{{int64(1), "example.com", "traditional", true, int64(1073741824), "restmail", "", "none", stamp, stamp}}}, nil
	case strings.Contains(query, `FROM "quota_usage"`):
		if id == "5" {
			return nil, errors.New("sensitive quota preload failure")
		}
		r := &authReadSnapshotRows{columns: []string{"mailbox_id", "subject_bytes", "body_bytes", "attachment_bytes", "message_count", "updated_at"}}
		if id == "6" {
			r.rows = [][]driver.Value{{int64(6), int64(12), int64(34), int64(275), int64(3), stamp}}
		}
		return r, nil
	}
	return nil, fmt.Errorf("unexpected mailbox lookup SQL: %s", query)
}

func mailboxLookupRouters(t *testing.T) (map[string]http.Handler, *auth.JWTService) {
	t.Helper()
	unavailable, failed, _, jwt := authReadBatchRouters(t)
	pool := sql.OpenDB(mailboxLookupConnection{})
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
	scoped := func(scope string, domain bool) http.Handler {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				entries := []grants.Entry{{Capability: middleware.CapMailboxesRead, Scope: scope}}
				if domain {
					entries = append(entries, grants.Entry{Capability: middleware.CapDomainsRead, Scope: grants.Domain("example.com")})
				}
				return grants.HeldOf(entries...), nil
			}
		}
		return makeRouter()
	}
	return map[string]http.Handler{
		"found": global, "inactive": global, "quota_present": global, "missing": global, "internal_error": global, "domain_unavailable": global, "quota_unavailable": global,
		"unauthenticated": unavailable, "session_unavailable": unavailable, "permission_store_unavailable": scoped(grants.Mailbox("user@example.com"), true),
		"request_too_large": global, "invalid_id": global, "address_empty": global, "address_missing": global, "not_local": global, "invalid_address": global, "address_unavailable": failed,
		"mailbox_scope": scoped(grants.Mailbox("user@example.com"), true), "domain_scope": scoped(grants.Domain("example.com"), true), "outside_scope": scoped(grants.Mailbox("other@example.com"), false), "domain_redacted": scoped(grants.Mailbox("user@example.com"), false),
	}, jwt
}

func TestMailboxLookupDatabaseFailuresAreNotMissingResources(t *testing.T) {
	routers, jwt := mailboxLookupRouters(t)
	for _, id := range []string{"3", "4", "5"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/mailboxes/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+mailboxToken(t, jwt))
		rr := httptest.NewRecorder()
		routers["found"].ServeHTTP(rr, req)
		if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Failed to get mailbox\"}}\n" {
			t.Errorf("ID %s: got %d %s, want sanitized500", id, rr.Code, rr.Body.String())
		}
	}
}

func TestMailboxLookupExamples(t *testing.T) {
	routers, jwt := mailboxLookupRouters(t)
	token := mailboxToken(t, jwt)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range mailboxLookupPaths {
		if len(pairs[operation]) == 0 {
			t.Fatalf("no examples for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				handler, ok := routers[name]
				if !ok {
					t.Fatalf("missing real precondition %s", name)
				}
				uri := path
				if id, ok := pair.Request.Path["id"]; ok {
					value := fmt.Sprint(id)
					if numeric, ok := id.(float64); ok {
						value = strconv.FormatFloat(numeric, 'f', -1, 64)
					}
					uri = strings.ReplaceAll(uri, "{id}", value)
				}
				query := url.Values{}
				for key, value := range pair.Request.Query {
					query.Set(key, fmt.Sprint(value))
				}
				if len(query) > 0 {
					uri += "?" + query.Encode()
				}
				req := httptest.NewRequest(http.MethodGet, uri, nil)
				if operation == "getMailbox" && name != "unauthenticated" {
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
					t.Fatal("response media type differs")
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, ok := mailboxLookupPaths[warning.OperationID]; ok {
				t.Errorf("lookup still has warning: %v", warning)
			}
		}
	}
}

func TestMailboxLookupExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for mailbox lookup exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	routers, jwt := mailboxLookupRouters(t)
	token := mailboxToken(t, jwt)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range mailboxLookupPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				credential := token
				if operation == "checkMailboxAddress" || name == "unauthenticated" {
					credential = ""
				}
				checkMailboxLookupWithVertrag(t, binary, routers[name], credential, path, name, pair)
			})
		}
	}
}
