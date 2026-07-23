package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ── Test harness ──────────────────────────────────────────────────────
//
// The router tests exercise AUTHORIZATION wiring only: which token types
// get past the middleware for each route group. They run against a
// *gorm.DB whose connections always fail, so:
//
//   - DENY cases are exact: the middleware rejects before any handler
//     (and therefore any DB access) runs, so 401/403 with the middleware's
//     error message proves the gate works.
//   - ALLOW cases assert the request REACHED the handler: any status is
//     fine except an auth-middleware rejection. Handlers typically answer
//     500 (DB down), 400 (validation), or a handler-level 403 whose
//     message differs from the middleware's.

type failDriver struct{}

func (failDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("no database in router tests")
}

type failConnector struct{}

func (failConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("no database in router tests")
}

func (failConnector) Driver() driver.Driver { return failDriver{} }

func newTestRouter(t *testing.T) (http.Handler, *auth.JWTService) {
	t.Helper()

	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(failConnector{}),
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to open gorm with failing connector: %v", err)
	}

	jwtSvc := auth.NewJWTService("router-test-secret", 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins: []string{"http://localhost:3000"},
		Environment:        "test",
	}
	return NewRouter(gdb, jwtSvc, cfg, nil), jwtSvc
}

// Capability sets mirroring the roles seeded by cmd/seed (seedRBAC).
var (
	adminRoleCaps = []string{
		"domains:read", "domains:write", "domains:delete",
		"mailboxes:read", "mailboxes:write", "mailboxes:delete",
		"pipelines:read", "pipelines:write", "pipelines:delete",
		"users:read", "messages:read",
		"queue:read", "queue:manage",
		"bans:read", "bans:write", "bans:delete",
	}
	readonlyRoleCaps = []string{
		"domains:read", "mailboxes:read", "pipelines:read",
		"users:read", "messages:read", "queue:read", "bans:read",
	}
)

func mailboxToken(t *testing.T, jwtSvc *auth.JWTService) string {
	t.Helper()
	pair, err := jwtSvc.GenerateTokenPair(10, "user@example.com", 1, false)
	if err != nil {
		t.Fatalf("failed to generate mailbox token: %v", err)
	}
	return pair.AccessToken
}

func legacyAdminMailboxToken(t *testing.T, jwtSvc *auth.JWTService) string {
	t.Helper()
	pair, err := jwtSvc.GenerateTokenPair(10, "legacy-admin@example.com", 1, true)
	if err != nil {
		t.Fatalf("failed to generate legacy admin mailbox token: %v", err)
	}
	return pair.AccessToken
}

func adminToken(t *testing.T, jwtSvc *auth.JWTService, caps []string) string {
	t.Helper()
	pair, err := jwtSvc.GenerateAdminTokenPair(1, "admin", caps)
	if err != nil {
		t.Fatalf("failed to generate admin token: %v", err)
	}
	return pair.AccessToken
}

func doRequest(router http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

// middlewareDenialMessages are the exact error messages the auth middleware
// produces. Any 403 carrying a different message came from a handler, which
// means authorization admitted the request.
var middlewareDenialMessages = map[string]bool{
	"Authentication required":  true,
	"Admin access required":    true,
	"Insufficient permissions": true,
}

func errorMessage(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var resp middleware.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		return ""
	}
	return resp.Error.Message
}

// assertMiddlewareDenied asserts the request was rejected by the auth
// middleware with the given status and message.
func assertMiddlewareDenied(t *testing.T, rr *httptest.ResponseRecorder, wantStatus int, wantMessage string) {
	t.Helper()
	if rr.Code != wantStatus {
		t.Fatalf("expected status %d, got %d (body %s)", wantStatus, rr.Code, rr.Body.String())
	}
	if msg := errorMessage(t, rr); msg != wantMessage {
		t.Errorf("expected denial message %q, got %q", wantMessage, msg)
	}
}

// assertReachedHandler asserts the request got past the auth middleware:
// not a 401, not a 403 carrying a middleware denial message, and not a
// chi router miss (which would mean the asserted path does not exist and
// the test proves nothing).
func assertReachedHandler(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("request rejected with 401, expected to reach handler (body %s)", rr.Body.String())
	}
	if rr.Code == http.StatusForbidden {
		if msg := errorMessage(t, rr); middlewareDenialMessages[msg] {
			t.Fatalf("request rejected by auth middleware (%q), expected to reach handler", msg)
		}
	}
	// chi's default NotFound/MethodNotAllowed responses are plain text;
	// handler-level 404s in this API are JSON respond.Error bodies.
	if rr.Code == http.StatusNotFound && strings.Contains(rr.Body.String(), "404 page not found") {
		t.Fatalf("route does not exist (chi 404); check the test path")
	}
	if rr.Code == http.StatusMethodNotAllowed {
		t.Fatalf("method not registered for route (chi 405); check the test method")
	}
}

// ── DENY: mailbox tokens on the admin surface ─────────────────────────

func TestAdminRoutes_MailboxTokenDenied(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	token := mailboxToken(t, jwtSvc)

	routes := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/admin/stats"},
		{http.MethodGet, "/api/v1/admin/admin-users"},
		{http.MethodPost, "/api/v1/admin/admin-users"},
		{http.MethodGet, "/api/v1/admin/roles"},
		{http.MethodGet, "/api/v1/admin/capabilities"},
		{http.MethodGet, "/api/v1/admin/domains"},
		{http.MethodPost, "/api/v1/admin/domains"},
		{http.MethodDelete, "/api/v1/admin/domains/1"},
		{http.MethodGet, "/api/v1/admin/mailboxes"},
		{http.MethodPost, "/api/v1/admin/mailboxes"},
		{http.MethodGet, "/api/v1/admin/aliases"},
		{http.MethodGet, "/api/v1/admin/webmail-accounts"},
		{http.MethodGet, "/api/v1/admin/pipelines"},
		{http.MethodGet, "/api/v1/admin/custom-filters"},
		{http.MethodGet, "/api/v1/admin/queue"},
		{http.MethodGet, "/api/v1/admin/queue/stats"},
		{http.MethodPost, "/api/v1/admin/queue/1/retry"},
		{http.MethodGet, "/api/v1/admin/domains/1/allowlist"},
		{http.MethodGet, "/api/v1/admin/domains/1/mta-sts"},
		{http.MethodGet, "/api/v1/admin/tls-reports"},
		{http.MethodGet, "/api/v1/admin/dkim"},
		{http.MethodGet, "/api/v1/admin/certificates"},
		{http.MethodGet, "/api/v1/admin/bans"},
		{http.MethodPost, "/api/v1/admin/bans"},
		{http.MethodGet, "/api/v1/admin/logs/delivery"},
		{http.MethodGet, "/api/v1/admin/logs/activity"},
		{http.MethodPost, "/api/v1/admin/test/send"},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rr := doRequest(router, rt.method, rt.path, token, "")
			assertMiddlewareDenied(t, rr, http.StatusForbidden, "Admin access required")
		})
	}
}

func TestAdminRoutes_NoTokenUnauthorized(t *testing.T) {
	router, _ := newTestRouter(t)

	for _, path := range []string{
		"/api/v1/admin/stats",
		"/api/v1/admin/domains",
		"/api/v1/admin/queue",
	} {
		t.Run(path, func(t *testing.T) {
			rr := doRequest(router, http.MethodGet, path, "", "")
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rr.Code)
			}
		})
	}
}

// ── DENY: capability granularity within admin tokens ──────────────────

func TestAdminRoutes_ReadonlyRoleDeniedOnWrites(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	token := adminToken(t, jwtSvc, readonlyRoleCaps)

	routes := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/v1/admin/domains"},
		{http.MethodPatch, "/api/v1/admin/domains/1"},
		{http.MethodDelete, "/api/v1/admin/domains/1"},
		{http.MethodPost, "/api/v1/admin/mailboxes"},
		{http.MethodPatch, "/api/v1/admin/mailboxes/1"},
		{http.MethodDelete, "/api/v1/admin/mailboxes/1"},
		{http.MethodPost, "/api/v1/admin/aliases"},
		{http.MethodDelete, "/api/v1/admin/aliases/1"},
		{http.MethodPost, "/api/v1/admin/webmail-accounts"},
		{http.MethodDelete, "/api/v1/admin/webmail-accounts/1"},
		{http.MethodPost, "/api/v1/admin/admin-users"},
		{http.MethodPut, "/api/v1/admin/admin-users/1"},
		{http.MethodDelete, "/api/v1/admin/admin-users/1"},
		{http.MethodPost, "/api/v1/admin/pipelines"},
		{http.MethodDelete, "/api/v1/admin/pipelines/1"},
		{http.MethodPost, "/api/v1/admin/pipelines/test"},
		{http.MethodPost, "/api/v1/admin/custom-filters"},
		{http.MethodDelete, "/api/v1/admin/custom-filters/1"},
		{http.MethodPost, "/api/v1/admin/queue/bulk-retry"},
		{http.MethodPost, "/api/v1/admin/queue/1/retry"},
		{http.MethodDelete, "/api/v1/admin/queue/1"},
		{http.MethodPost, "/api/v1/admin/domains/1/allowlist"},
		{http.MethodPut, "/api/v1/admin/domains/1/mta-sts"},
		{http.MethodPut, "/api/v1/admin/dkim/1"},
		{http.MethodPost, "/api/v1/admin/certificates"},
		{http.MethodDelete, "/api/v1/admin/certificates/1"},
		{http.MethodPost, "/api/v1/admin/bans"},
		{http.MethodDelete, "/api/v1/admin/bans/1"},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rr := doRequest(router, rt.method, rt.path, token, "")
			assertMiddlewareDenied(t, rr, http.StatusForbidden, "Insufficient permissions")
		})
	}
}

func TestAdminRoutes_AdminRoleDeniedOnAdminUserManagement(t *testing.T) {
	// The seeded "admin" role has users:read only; creating, updating, and
	// deleting admin users requires users:write / users:delete (superadmin).
	router, jwtSvc := newTestRouter(t)
	token := adminToken(t, jwtSvc, adminRoleCaps)

	routes := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/v1/admin/admin-users"},
		{http.MethodPut, "/api/v1/admin/admin-users/1"},
		{http.MethodDelete, "/api/v1/admin/admin-users/1"},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rr := doRequest(router, rt.method, rt.path, token, "")
			assertMiddlewareDenied(t, rr, http.StatusForbidden, "Insufficient permissions")
		})
	}
}

// ── ALLOW: admin tokens keep their capability-scoped surfaces ─────────

func TestAdminRoutes_ReadonlyRoleAllowedOnReads(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	token := adminToken(t, jwtSvc, readonlyRoleCaps)

	paths := []string{
		"/api/v1/admin/stats",
		"/api/v1/admin/admin-users",
		"/api/v1/admin/roles",
		"/api/v1/admin/capabilities",
		"/api/v1/admin/domains",
		"/api/v1/admin/mailboxes",
		"/api/v1/admin/aliases",
		"/api/v1/admin/webmail-accounts",
		"/api/v1/admin/pipelines",
		"/api/v1/admin/custom-filters",
		"/api/v1/admin/queue",
		"/api/v1/admin/queue/stats",
		"/api/v1/admin/domains/1/allowlist",
		"/api/v1/admin/domains/1/mta-sts",
		"/api/v1/admin/tls-reports",
		"/api/v1/admin/dkim",
		"/api/v1/admin/certificates",
		"/api/v1/admin/bans",
		"/api/v1/admin/logs/delivery",
		"/api/v1/admin/logs/activity",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			rr := doRequest(router, http.MethodGet, path, token, "")
			assertReachedHandler(t, rr)
		})
	}
}

func TestAdminRoutes_AdminRoleAllowedOnManagedSurfaces(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	token := adminToken(t, jwtSvc, adminRoleCaps)

	routes := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/v1/admin/domains", `{"name":"x.test"}`},
		{http.MethodPatch, "/api/v1/admin/mailboxes/1", `{"display_name":"x"}`},
		{http.MethodDelete, "/api/v1/admin/mailboxes/1", ""},
		{http.MethodPost, "/api/v1/admin/aliases", `{}`},
		{http.MethodPost, "/api/v1/admin/pipelines", `{}`},
		{http.MethodPost, "/api/v1/admin/queue/bulk-retry", `{}`},
		{http.MethodPost, "/api/v1/admin/queue/1/retry", ""},
		{http.MethodPost, "/api/v1/admin/bans", `{}`},
		{http.MethodDelete, "/api/v1/admin/bans/1", ""},
		{http.MethodPut, "/api/v1/admin/dkim/1", `{}`},
		{http.MethodPost, "/api/v1/admin/certificates", `{}`},
		{http.MethodGet, "/api/v1/admin/admin-users", ""},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rr := doRequest(router, rt.method, rt.path, token, rt.body)
			assertReachedHandler(t, rr)
		})
	}
}

func TestAdminRoutes_SuperadminWildcardAllowedEverywhere(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	token := adminToken(t, jwtSvc, []string{"*"})

	routes := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/admin/stats", ""},
		{http.MethodPost, "/api/v1/admin/admin-users", `{}`},
		{http.MethodDelete, "/api/v1/admin/admin-users/1", ""},
		{http.MethodDelete, "/api/v1/admin/domains/1", ""},
		{http.MethodDelete, "/api/v1/admin/mailboxes/1", ""},
		{http.MethodDelete, "/api/v1/admin/queue/1", ""},
		{http.MethodDelete, "/api/v1/admin/pipelines/1", ""},
		{http.MethodPost, "/api/v1/admin/test/send", `{}`},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rr := doRequest(router, rt.method, rt.path, token, rt.body)
			assertReachedHandler(t, rr)
		})
	}
}

func TestAdminRoutes_LegacyIsAdminMailboxRetainsAccess(t *testing.T) {
	// Mailbox tokens with the deprecated IsAdmin flag had full admin access
	// before capability wiring; they must keep it (pre-RBAC compatibility).
	router, jwtSvc := newTestRouter(t)
	token := legacyAdminMailboxToken(t, jwtSvc)

	routes := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/admin/domains", ""},
		{http.MethodPost, "/api/v1/admin/domains", `{"name":"x.test"}`},
		{http.MethodGet, "/api/v1/admin/queue", ""},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rr := doRequest(router, rt.method, rt.path, token, rt.body)
			assertReachedHandler(t, rr)
		})
	}
}

// ── Regression: mailbox-scoped webmail/gateway surface still works ────

func TestMailboxRoutes_MailboxTokenReachesHandlers(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	token := mailboxToken(t, jwtSvc)

	routes := []struct {
		method, path, body string
	}{
		// Routes the IMAP/POP3 gateways call via apiclient.
		{http.MethodGet, "/api/v1/accounts/1/folders", ""},
		{http.MethodGet, "/api/v1/accounts/1/folders/INBOX/messages", ""},
		{http.MethodGet, "/api/v1/messages/1", ""},
		{http.MethodGet, "/api/v1/messages/1/raw", ""},
		{http.MethodPatch, "/api/v1/messages/1", `{"is_read":true}`},
		{http.MethodDelete, "/api/v1/messages/1", ""},
		{http.MethodGet, "/api/v1/accounts/1/quota", ""},
		// Webmail-critical routes.
		{http.MethodGet, "/api/v1/accounts", ""},
		{http.MethodPost, "/api/v1/messages/send", `{}`},
		{http.MethodGet, "/api/v1/accounts/1/vacation", ""},
		{http.MethodGet, "/api/v1/accounts/1/sieve", ""},
		{http.MethodGet, "/api/v1/accounts/1/search?q=x", ""},
		{http.MethodGet, "/api/v1/accounts/1/contacts", ""},
		{http.MethodGet, "/api/v1/accounts/1/quarantine", ""},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			rr := doRequest(router, rt.method, rt.path, token, rt.body)
			assertReachedHandler(t, rr)
		})
	}
}

func TestMailboxRoutes_NoTokenUnauthorized(t *testing.T) {
	router, _ := newTestRouter(t)

	rr := doRequest(router, http.MethodGet, "/api/v1/accounts", "", "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// ── Regression: SSE events endpoint auth ──────────────────────────────

func TestSSEEvents_NoTokenUnauthorized(t *testing.T) {
	router, _ := newTestRouter(t)

	rr := doRequest(router, http.MethodGet, "/api/v1/accounts/1/events", "", "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestSSEEvents_MailboxTokenReachesHandler(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	token := mailboxToken(t, jwtSvc)

	rr := doRequest(router, http.MethodGet, "/api/v1/accounts/1/events", token, "")
	assertReachedHandler(t, rr)
}

// ── Regression: gateway inbound flow stays unauthenticated ────────────

func TestGatewayInbound_CheckAddressNoAuthRequired(t *testing.T) {
	router, _ := newTestRouter(t)

	rr := doRequest(router, http.MethodGet, "/api/mailboxes?address=user@example.com", "", "")
	assertReachedHandler(t, rr)
}

func TestGatewayInbound_DeliverNoAuthRequired(t *testing.T) {
	router, _ := newTestRouter(t)

	body := `{"address":"user@example.com","sender":"peer@remote.test","subject":"hi","body_text":"hello"}`
	rr := doRequest(router, http.MethodPost, "/api/v1/messages/deliver", "", body)
	assertReachedHandler(t, rr)
}

// ── Regression: token refresh for both token types ────────────────────

func refreshWithCookie(router http.Handler, refreshToken string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "restmail_refresh", Value: refreshToken})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func decodeAccessToken(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode refresh response: %v", err)
	}
	return resp.Data.AccessToken
}

func TestRefresh_MailboxToken(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	pair, err := jwtSvc.GenerateTokenPair(10, "user@example.com", 1, false)
	if err != nil {
		t.Fatalf("failed to generate mailbox pair: %v", err)
	}

	rr := refreshWithCookie(router, pair.RefreshToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body %s)", rr.Code, rr.Body.String())
	}

	claims, err := jwtSvc.ValidateAccessToken(decodeAccessToken(t, rr))
	if err != nil {
		t.Fatalf("refreshed access token invalid: %v", err)
	}
	if claims.UserType != "mailbox" {
		t.Errorf("expected UserType mailbox, got %q", claims.UserType)
	}
	if claims.MailboxID != 10 {
		t.Errorf("expected MailboxID 10, got %d", claims.MailboxID)
	}
}

func TestRefresh_AdminTokenPreservesCapabilities(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	pair, err := jwtSvc.GenerateAdminTokenPair(1, "admin", []string{"domains:read", "queue:read"})
	if err != nil {
		t.Fatalf("failed to generate admin pair: %v", err)
	}

	rr := refreshWithCookie(router, pair.RefreshToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body %s)", rr.Code, rr.Body.String())
	}

	claims, err := jwtSvc.ValidateAccessToken(decodeAccessToken(t, rr))
	if err != nil {
		t.Fatalf("refreshed access token invalid: %v", err)
	}
	if claims.UserType != "admin" {
		t.Errorf("expected UserType admin, got %q", claims.UserType)
	}
	if len(claims.Capabilities) != 2 || claims.Capabilities[0] != "domains:read" {
		t.Errorf("expected capabilities preserved, got %v", claims.Capabilities)
	}
}
