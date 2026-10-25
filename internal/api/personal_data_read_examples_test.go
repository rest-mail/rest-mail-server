package api

import (
	"context"
	"database/sql/driver"
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
	"github.com/restmail/restmail"
)

var personalDataReadPaths = map[string]string{
	"listContacts":       "/api/v1/accounts/{id}/contacts",
	"listCalendarEvents": "/api/v1/accounts/{id}/calendar-events",
}

type personalDataReadConnection struct {
	accountCredentialListConnection
}

func (c personalDataReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c personalDataReadConnection) Driver() driver.Driver                        { return c }
func (c personalDataReadConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c personalDataReadConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "JOIN role_capabilities") {
		if c.mode == "domain_manager" {
			return &authReadSnapshotRows{columns: []string{"scope", "capability", "expires_at", "activation_expires_at"}, rows: [][]driver.Value{{"domain:example.com", "mail:read", nil, nil}}}, nil
		}
		rows, err := c.accountCredentialListConnection.QueryContext(ctx, query, args)
		if err == nil && c.mode == "normal" {
			r := rows.(*authReadSnapshotRows)
			r.rows = append(r.rows, []driver.Value{"mailbox:account5@example.com", "mail:read", nil, nil})
		}
		return rows, err
	}
	contacts, calendar := strings.Contains(query, `FROM "contacts"`), strings.Contains(query, `FROM "calendar_event_versions"`)
	if !contacts && !calendar {
		return c.accountCredentialListConnection.QueryContext(ctx, query, args)
	}
	if len(args) == 0 || !strings.Contains(query, "mailbox_id =") {
		return nil, errors.New("personal data lost account restriction")
	}
	id := fmt.Sprint(args[0].Value)
	if id == "4" {
		return nil, errors.New("private personal data query detail")
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if calendar {
		if !strings.Contains(query, "ORDER BY uid, sequence DESC") {
			return nil, errors.New("calendar lost version ordering")
		}
		r := &authReadSnapshotRows{columns: []string{"uid", "sequence", "method", "status", "summary", "message_id"}}
		if id != "2" {
			r.rows = [][]driver.Value{
				{"event-a", int64(3), "REQUEST", "CONFIRMED", "Latest café", int64(77)},
				{"event-a", int64(2), "CANCEL", "CANCELLED", "Earlier", int64(76)},
				{"event-a", int64(1), "REQUEST", "TENTATIVE", "Original", int64(75)},
				{"event-b", int64(1), "REQUEST", "CANCELLED", "Cancelled", nil},
				{"event-c", int64(0), "REQUEST", "TENTATIVE", "Tentative", int64(88)},
			}
		}
		return r, nil
	}
	filtered, paged := false, false
	for _, arg := range args {
		if text, ok := arg.Value.(string); ok && text == "trusted" {
			filtered = true
		}
	}
	if filtered {
		if !strings.Contains(query, "trust_level =") || !strings.Contains(query, "email LIKE") || !strings.Contains(query, "ESCAPE") {
			return nil, errors.New("contacts lost filter or literal search")
		}
		found := false
		for _, arg := range args {
			if arg.Value == `%100\%\_\\%` {
				found = true
			}
		}
		if !found {
			return nil, errors.New("contacts did not escape LIKE metacharacters")
		}
	}
	if strings.Contains(query, "count(*)") {
		if id == "5" {
			return nil, errors.New("private contact count detail")
		}
		total := int64(2)
		if id == "2" {
			total = 0
		}
		if filtered {
			total = 1
		}
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{total}}}, nil
	}
	if !strings.Contains(query, "ORDER BY email ASC") || !strings.Contains(query, "LIMIT") {
		return nil, errors.New("contacts lost sorting or bound")
	}
	if strings.Contains(query, "OFFSET") {
		paged = true
	}
	limitAt := len(args) - 1
	if paged {
		if args[limitAt].Value != int64(1) {
			return nil, errors.New("contacts lost requested offset")
		}
		limitAt--
	}
	limit, ok := args[limitAt].Value.(int64)
	if !ok || (paged && limit != 1) || (!paged && limit != 1 && limit != 50 && limit != 200) {
		return nil, errors.New("contacts lost requested or bounded limit")
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "email", "name", "trust_level", "source", "created_at", "updated_at"}}
	if id != "2" {
		r.rows = [][]driver.Value{{int64(31), n, "\"a100%_\\\\\"@example.com", "Friend café", "trusted", "manual", stamp, stamp}, {int64(32), n, "z@example.com", "Blocked", "blocked", "import", stamp, stamp}}
		if filtered {
			r.rows = r.rows[:1]
		}
		if paged {
			r.rows = r.rows[1:]
		} else if limit == 1 {
			r.rows = r.rows[:1]
		}
	}
	return r, nil
}

func personalDataReadScenarios(t *testing.T) map[string]accountReadScenario {
	t.Helper()
	out := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		return personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}
	})
	for _, name := range []string{"filtered", "paged", "first_page", "clamped", "invalid_trust", "invalid_limit", "count_unavailable", "blank_search"} {
		out[name] = out["found"]
	}
	out["domain_grant"] = out["domain_manager"]
	out["app_password_other"] = out["app_password"]
	return out
}

func TestPersonalDataReadFailuresAreNotDeniedOrEmpty(t *testing.T) {
	scenarios := personalDataReadScenarios(t)
	for _, tc := range []struct{ path, scenario string }{
		{"/api/v1/accounts/3/contacts", "found"},
		{"/api/v1/accounts/1/contacts", "grant_unavailable"},
		{"/api/v1/accounts/1/contacts", "ownership_unavailable"},
		{"/api/v1/accounts/4/calendar-events", "found"},
		{"/api/v1/accounts/5/contacts", "found"},
	} {
		scenario := scenarios[tc.scenario]
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+scenario.token)
		rr := httptest.NewRecorder()
		scenario.handler.ServeHTTP(rr, req)
		if rr.Code != 500 || strings.Contains(rr.Body.String(), "private ") {
			t.Errorf("%s/%s: got %d %s, want sanitized 500", tc.path, tc.scenario, rr.Code, rr.Body.String())
		}
	}
}

func TestPersonalDataReadExamples(t *testing.T) {
	scenarios := personalDataReadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range personalDataReadPaths {
		if len(pairs[operation]) < 16 {
			t.Fatalf("incomplete examples for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				scenario, ok := scenarios[name]
				if !ok {
					t.Fatalf("no scenario for %s", name)
				}
				req := httptest.NewRequest(http.MethodGet, publicDiscoveryURI(domainAliasReadURI(path, pair), pair), nil)
				if scenario.token != "" {
					req.Header.Set("Authorization", "Bearer "+scenario.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				scenario.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
				}
				if name == "rate_limited" {
					retry, err := strconv.Atoi(rr.Header().Get("Retry-After"))
					if err != nil || retry < 1 {
						t.Fatal("refusal lost Retry-After")
					}
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, warning := range g.Report() {
			if _, ok := personalDataReadPaths[warning.OperationID]; ok {
				t.Errorf("personal data warning: %v", warning)
			}
		}
	}
}

func TestPersonalDataReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for personal data read exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := personalDataReadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range personalDataReadPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				scenario := scenarios[name]
				checkMailboxLookupWithVertrag(t, binary, scenario.handler, scenario.token, path, name, pair)
			})
		}
	}
}
