package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

type contactExportExampleConnection struct {
	accountCredentialListConnection
}

func (c contactExportExampleConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c contactExportExampleConnection) Driver() driver.Driver                        { return c }
func (c contactExportExampleConnection) Open(string) (driver.Conn, error)             { return c, nil }

func (c contactExportExampleConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, `FROM "contacts"`) {
		return c.accountCredentialListConnection.QueryContext(ctx, query, args)
	}
	if len(args) != 1 || !strings.Contains(query, `WHERE mailbox_id =`) {
		return nil, errors.New("contact export lost its mailbox restriction")
	}
	switch args[0].Value {
	case int64(1):
		return &authReadSnapshotRows{
			columns: []string{"id", "email", "name"},
			rows:    [][]driver.Value{{int64(31), "elodie@example.test", "Elodie Example"}},
		}, nil
	case int64(4):
		return nil, errors.New("private contact query failure detail")
	default:
		return nil, errors.New("unexpected mailbox in export query")
	}
}

func TestContactExportExamples(t *testing.T) {
	const operation = "exportContacts"
	route := requirementRoute{http.MethodGet, "/api/v1/accounts/{id}/contacts/export.vcf"}
	scenarios := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		return contactExportExampleConnection{accountCredentialListConnection{mode: mode, keys: keys}}
	})
	scenarios["downloaded"] = scenarios["key_read"]
	scenarios["query_unavailable"] = scenarios["found"]
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs[operation] {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, operation, name)
			scenario, ok := scenarios[name]
			if !ok {
				t.Fatalf("no export scenario for %s", name)
			}
			req := requirementRequest(route, pair)
			if scenario.token != "" {
				req.Header.Set("Authorization", "Bearer "+scenario.token)
			}
			if name == "request_too_large" {
				req.ContentLength = 9
			}
			rr := httptest.NewRecorder()
			scenario.handler.ServeHTTP(rr, req)
			if pair.Response.Status == http.StatusOK {
				var want string
				if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
					t.Fatal(err)
				}
				if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != pair.Response.MediaType+"; charset=utf-8" ||
					rr.Header().Get("Content-Disposition") != "attachment; filename=contacts.vcf" || rr.Body.String() != want {
					t.Fatalf("download: got %d %v %q, want 200 with %q", rr.Code, rr.Header(), rr.Body, want)
				}
				return
			}
			assertRequirementResponse(t, pair, rr)
			if name == "rate_limited" {
				if retry, err := strconv.Atoi(rr.Header().Get("Retry-After")); err != nil || retry < 1 {
					t.Fatal("rate refusal lacks Retry-After")
				}
			}
		})
	}
}
