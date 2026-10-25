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

var folderQuarantineReadPaths = map[string]string{
	"listFolders":    "/api/v1/accounts/{id}/folders",
	"listQuarantine": "/api/v1/accounts/{id}/quarantine",
}

// Controlled rows prove production authorization, SQL restrictions, mapping
// and error wiring. Database aggregation/filtering/ordering need PostgreSQL.
type folderQuarantineReadConnection struct{ personalDataReadConnection }

func (c folderQuarantineReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c folderQuarantineReadConnection) Driver() driver.Driver                        { return c }
func (c folderQuarantineReadConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c folderQuarantineReadConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	folders, quarantine := strings.Contains(query, `FROM "messages"`), strings.Contains(query, `FROM "quarantine"`)
	if !folders && !quarantine {
		return c.personalDataReadConnection.QueryContext(ctx, query, args)
	}
	if len(args) == 0 || !strings.Contains(query, "mailbox_id =") {
		return nil, errors.New("mail list lost account restriction")
	}
	id := fmt.Sprint(args[0].Value)
	if folders {
		for _, part := range []string{"COUNT(CASE WHEN is_deleted = false", "is_read = false", "GROUP BY", "ORDER BY folder ASC"} {
			if !strings.Contains(query, part) {
				return nil, fmt.Errorf("folder summary lost %s", part)
			}
		}
		if id == "4" {
			return nil, errors.New("private folder aggregation detail")
		}
		r := &authReadSnapshotRows{columns: []string{"folder", "total", "unread"}}
		if id != "2" {
			r.rows = [][]driver.Value{{"Archive", int64(2), int64(1)}, {"INBOX", int64(5), int64(2)}, {"Projects/2026", int64(0), int64(0)}}
		}
		return r, nil
	}
	if !strings.Contains(query, "released = false") {
		return nil, errors.New("quarantine lost unreleased restriction")
	}
	if strings.Contains(query, "count(*)") {
		if id == "5" {
			return nil, errors.New("private quarantine count detail")
		}
		total := int64(2)
		if id == "2" {
			total = 0
		}
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{total}}}, nil
	}
	if id == "4" {
		return nil, errors.New("private quarantine list detail")
	}
	if !strings.Contains(query, "ORDER BY received_at DESC") || !strings.Contains(query, "LIMIT") {
		return nil, errors.New("quarantine lost ordering or bound")
	}
	index := len(args) - 1
	paged := strings.Contains(query, "OFFSET")
	if paged {
		if args[index].Value != int64(1) {
			return nil, errors.New("quarantine lost offset")
		}
		index--
	}
	limit, ok := args[index].Value.(int64)
	if !ok || (limit != 1 && limit != 50 && limit != 200) {
		return nil, errors.New("quarantine lost bounded limit")
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	stamp := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	r := &authReadSnapshotRows{columns: []string{"id", "mailbox_id", "sender", "subject", "body_preview", "spam_score", "quarantine_reason", "received_at", "expires_at", "released", "raw_message"}}
	if id != "2" {
		r.rows = [][]driver.Value{{int64(41), n, "sender@example.net", "Held café", "Preview only", float64(4.5), "spam", stamp, stamp.AddDate(0, 0, 7), false, "private-raw-message-sentinel"}, {int64(40), n, "other@example.net", "No score", "Review this", nil, "custom_filter", stamp.AddDate(0, 0, -1), stamp.AddDate(0, 0, 6), false, "private-raw-message-sentinel"}}
		if paged {
			r.rows = r.rows[1:]
		} else if limit == 1 {
			r.rows = r.rows[:1]
		}
	}
	return r, nil
}

func folderQuarantineReadScenarios(t *testing.T) map[string]accountReadScenario {
	t.Helper()
	out := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		return folderQuarantineReadConnection{personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}}
	})
	for _, name := range []string{"paged", "first_page", "clamped", "invalid_limit", "count_unavailable"} {
		out[name] = out["found"]
	}
	out["domain_grant"] = out["domain_manager"]
	out["app_password_other"] = out["app_password"]
	return out
}

func TestFolderQuarantineFailuresAreNotDeniedOrEmpty(t *testing.T) {
	scenarios := folderQuarantineReadScenarios(t)
	for _, tc := range []struct{ path, scenario string }{
		{"/api/v1/accounts/4/folders", "found"},
		{"/api/v1/accounts/3/quarantine", "found"},
		{"/api/v1/accounts/1/quarantine", "grant_unavailable"},
		{"/api/v1/accounts/1/quarantine", "ownership_unavailable"},
		{"/api/v1/accounts/4/quarantine", "found"},
		{"/api/v1/accounts/5/quarantine", "found"},
	} {
		scenario := scenarios[tc.scenario]
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+scenario.token)
		rr := httptest.NewRecorder()
		scenario.handler.ServeHTTP(rr, req)
		if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
			t.Errorf("%s/%s: got %d %s, want sanitized 500", tc.path, tc.scenario, rr.Code, rr.Body.String())
		}
	}
}

func TestFolderQuarantineReadExamples(t *testing.T) {
	scenarios := folderQuarantineReadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range folderQuarantineReadPaths {
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
			if _, ok := folderQuarantineReadPaths[warning.OperationID]; ok {
				t.Errorf("folder/quarantine warning: %v", warning)
			}
		}
	}
}

func TestFolderQuarantineReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for folder/quarantine read exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := folderQuarantineReadScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range folderQuarantineReadPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				scenario := scenarios[name]
				checkMailboxLookupWithVertrag(t, binary, scenario.handler, scenario.token, path, name, pair)
			})
		}
	}
}
