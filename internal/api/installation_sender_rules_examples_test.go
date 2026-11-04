package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/rest-mail/rest-mail-server/internal/api/limittest"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

const installationSenderRulesPath = "/api/v1/admin/sender-rules"

// installationSenderRulesConnection answers the production handler's list read
// with no rows, through real GORM mapping, and nothing else.
type installationSenderRulesConnection struct{ capabilityExampleConnection }

func (c installationSenderRulesConnection) Connect(context.Context) (driver.Conn, error) {
	return c, nil
}
func (c installationSenderRulesConnection) Driver() driver.Driver { return c }
func (c installationSenderRulesConnection) Open(string) (driver.Conn, error) {
	return c, nil
}
func (c installationSenderRulesConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, `"installation_sender_rules"`) {
		return nil, errors.New("unexpected table in the sender rule list")
	}
	return &authReadSnapshotRows{columns: []string{"id"}}, nil
}

type installationSenderRulesScenario struct {
	token  string
	router http.Handler
}

// installationSenderRulesScenarios are the production routers behind each pair of
// listInstallationSenderRules: an empty list, an unfinished setup, a failing
// database, a failing session lookup and a spent refusal budget.
func installationSenderRulesScenarios(t *testing.T) map[string]installationSenderRulesScenario {
	t.Helper()
	unavailable, failed, _, jwt := authReadBatchRouters(t)
	pool := sql.OpenDB(installationSenderRulesConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	empty := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
	t.Cleanup(empty.Close)
	session, err := jwt.GenerateSession(10, "operator@example.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := jwt.GenerateSession(10, "operator@example.test", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]installationSenderRulesScenario{
		"available":           {session.AccessToken, empty.Public},
		"unauthenticated":     {"", unavailable},
		"setup_required":      {pending.AccessToken, failed},
		"request_too_large":   {session.AccessToken, failed},
		"rate_limited":        {"", apiKeyRefusalLimited(t)},
		"internal_error":      {session.AccessToken, failed},
		"session_unavailable": {session.AccessToken, unavailable},
	}
}

// Each pair of the list answers as it says, through the production router
// (#1576). What a domain administrator gets is TestInstallationSenderRules_OperatorOnlyAndAudited's.
func TestInstallationSenderRuleListExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	const operation = "listInstallationSenderRules"
	scenarios := installationSenderRulesScenarios(t)
	if len(pairs[operation]) != len(scenarios) {
		t.Fatalf("%d pairs, %d scenarios", len(pairs[operation]), len(scenarios))
	}
	for name, pair := range pairs[operation] {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, operation, name)
			s, ok := scenarios[name]
			if !ok {
				t.Fatal("no scenario for this pair")
			}
			req := httptest.NewRequest(http.MethodGet, installationSenderRulesPath, nil)
			if s.token != "" {
				req.Header.Set("Authorization", "Bearer "+s.token)
			}
			if name == "request_too_large" {
				req.ContentLength = 9
			}
			rr := httptest.NewRecorder()
			s.router.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got %d, want %d: %s", rr.Code, pair.Response.Status, rr.Body)
			}
			if pair.Response.MediaType != rr.Header().Get("Content-Type") {
				t.Fatalf("Content-Type %q, want %q", rr.Header().Get("Content-Type"), pair.Response.MediaType)
			}
			if name == "rate_limited" {
				limittest.RetryAfter(t, rr.Header())
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
		})
	}
}

func TestInstallationSenderRuleListExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to run the real sender rule list boundary check")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := installationSenderRulesScenarios(t)
	for _, tc := range []struct{ scenario, status string }{
		{"available", "200"},
		{"unauthenticated", "401"},
		{"setup_required", "403"},
		{"request_too_large", "413"},
		{"rate_limited", "429"},
		{"internal_error", "500"},
		{"session_unavailable", "503"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			s := scenarios[tc.scenario]
			checkRegisteredExamplesWithVertrag(t, binary, s.router, s.token, http.MethodGet, tc.status,
				[]string{installationSenderRulesPath}, 1, tc.status == "413")
		})
	}
}
