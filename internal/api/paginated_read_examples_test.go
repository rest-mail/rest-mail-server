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
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

var paginatedReadPaths = map[string]string{
	"getDeliveryLogs":   "/api/v1/admin/logs/delivery",
	"listAliases":       "/api/v1/admin/aliases",
	"listAuditEntries":  "/api/v1/admin/audit",
	"listBans":          "/api/v1/admin/bans",
	"listCertificates":  "/api/v1/admin/certificates",
	"listCustomFilters": "/api/v1/admin/custom-filters",
	"listDomains":       "/api/v1/admin/domains",
	"listGrants":        "/api/v1/admin/grants",
	"listMailboxes":     "/api/v1/admin/mailboxes",
	"listPipelineLogs":  "/api/v1/admin/pipelines/logs",
	"listPipelines":     "/api/v1/admin/pipelines",
	"listQueue":         "/api/v1/admin/queue",
	"listTlsReports":    "/api/v1/admin/tls-reports",
}

// Share the real session-cache failure and controlled current/permitted session
// fixtures with the other authenticated reads. Only replace the SQL snapshot:
// these production list handlers read empty tables through GORM.
func paginatedReadRouters(t *testing.T) (http.Handler, http.Handler, http.Handler, *auth.JWTService) {
	t.Helper()
	unavailable, failed, _, jwt := authReadBatchRouters(t)
	pool := sql.OpenDB(paginatedReadConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
	t.Cleanup(r.Close)
	return unavailable, failed, r.Public, jwt
}

type paginatedReadConnection struct{ capabilityExampleConnection }

func (c paginatedReadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c paginatedReadConnection) Driver() driver.Driver                        { return c }
func (c paginatedReadConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c paginatedReadConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if !strings.HasPrefix(q, "select ") {
		return nil, errors.New("list fixture accepts only reads")
	}
	for _, table := range []string{"aliases", "audit_log", "bans", "certificates", "custom_filters", "domains", "grants", "grant_activations", "mailboxes", "pipelines", "message_traces", "outbound_queue", "tls_reports"} {
		if strings.Contains(q, `"`+table+`"`) {
			if strings.HasPrefix(q, "select count(*)") {
				return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}, nil
			}
			return &authReadSnapshotRows{columns: []string{"id"}}, nil
		}
	}
	return nil, errors.New("unexpected table in list response fixture")
}

func TestPaginatedReadExamples(t *testing.T) {
	unavailable, failed, empty, jwt := paginatedReadRouters(t)
	token := mailboxToken(t, jwt)
	pending, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range paginatedReadPaths {
		for _, tc := range []struct {
			name   string
			status int
			token  string
			router http.Handler
		}{
			{"available", 200, token, empty},
			{"unauthenticated", 401, "", unavailable},
			{"setup_required", 403, pending.AccessToken, failed},
			{"request_too_large", 413, token, failed},
			{"invalid_limit", 422, token, empty},
			{"internal_error", 500, token, failed},
			{"session_unavailable", 503, token, unavailable},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				pair, ok := pairs[operation][tc.name]
				examplepairs.Proven(t, operation, tc.name)
				if !ok {
					t.Fatal("named list pair is missing")
				}
				query := url.Values{}
				for name, value := range pair.Request.Query {
					query.Set(name, fmt.Sprint(value))
				}
				req := httptest.NewRequest(http.MethodGet, path+"?"+query.Encode(), nil)
				if tc.token != "" {
					req.Header.Set("Authorization", "Bearer "+tc.token)
				}
				if tc.status == 413 {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				tc.router.ServeHTTP(rr, req)
				if rr.Code != tc.status {
					t.Fatalf("got %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
				}
				if pair.Response.Status != rr.Code || pair.Response.MediaType != rr.Header().Get("Content-Type") {
					t.Fatal("response metadata differs from its pair")
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, generator := range gapGenerators(t) {
		for _, warning := range generator.Report() {
			if _, selected := paginatedReadPaths[warning.OperationID]; selected {
				t.Errorf("list still has a generator warning: %v", warning)
			}
		}
	}
}

func TestPaginatedReadExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to run the real list example boundary check")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	unavailable, failed, empty, jwt := paginatedReadRouters(t)
	token := mailboxToken(t, jwt)
	pending, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, path := range paginatedReadPaths {
		paths = append(paths, path)
	}
	for _, tc := range []struct {
		name, status, token string
		router              http.Handler
	}{
		{"empty collections", "200", token, empty},
		{"missing credentials", "401", "", unavailable},
		{"unfinished setup", "403", pending.AccessToken, failed},
		{"oversized transport body", "413", token, failed},
		{"invalid page size", "422", token, empty},
		{"failed stores", "500", token, failed},
		{"failed session lookup", "503", token, unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) { checkAuthReadWithVertrag(t, binary, tc.router, tc.token, tc.status, paths) })
	}
}
