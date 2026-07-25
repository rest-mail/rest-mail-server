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

	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// routerTestSecret is the HS256 secret newTestRouter's JWTService is built with;
// tests that forge raw tokens (e.g. a stale is_admin payload) sign with it.
const routerTestSecret = "router-test-secret"

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

// newFailingGormDB opens a gorm DB whose connections always fail, so handlers
// that touch the DB error out and DENY assertions stay exact (the middleware
// rejects before any DB access).
func newFailingGormDB(t *testing.T) *gorm.DB {
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
	return gdb
}

func newTestRouter(t *testing.T) (http.Handler, *auth.JWTService) {
	t.Helper()

	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins: []string{"http://localhost:3000"},
		Environment:        "test",
	}
	return NewRouter(newFailingGormDB(t), jwtSvc, cfg, nil), jwtSvc
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
		"observability:read",
	}
	readonlyRoleCaps = []string{
		"domains:read", "mailboxes:read", "pipelines:read",
		"users:read", "messages:read", "queue:read", "bans:read",
		"observability:read",
	}
)

func mailboxToken(t *testing.T, jwtSvc *auth.JWTService) string {
	t.Helper()
	pair, err := jwtSvc.GenerateTokenPair(10, "user@example.com", 1)
	if err != nil {
		t.Fatalf("failed to generate mailbox token: %v", err)
	}
	return pair.AccessToken
}

// legacyAdminMailboxToken forges a mailbox access token that still carries a
// stale is_admin payload, as tokens issued before OSI-14 did. The JWTService no
// longer mints such a token, so it is signed directly with the router test
// secret to prove the admin surface refuses it.
func legacyAdminMailboxToken(t *testing.T) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":        "mailbox:10",
		"iss":        "restmail",
		"exp":        time.Now().Add(5 * time.Minute).Unix(),
		"user_type":  "mailbox",
		"token_type": "access",
		"is_admin":   true,
		"mailbox_id": 10,
	})
	s, err := tok.SignedString([]byte(routerTestSecret))
	if err != nil {
		t.Fatalf("failed to sign legacy admin mailbox token: %v", err)
	}
	return s
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
		{http.MethodGet, "/api/v1/admin/pipelines/analytics"},
		{http.MethodGet, "/api/v1/admin/messages/1/trace"},
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
		"/api/v1/admin/pipelines/analytics",
		"/api/v1/admin/messages/1/trace",
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

func TestAdminRoutes_LegacyIsAdminMailboxDenied(t *testing.T) {
	// OSI-14: the deprecated mailbox is_admin escalation is removed. A mailbox
	// token still carrying a stale is_admin payload must now be REFUSED on the
	// admin surface (previously it retained full admin access).
	router, _ := newTestRouter(t)
	token := legacyAdminMailboxToken(t)

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
			assertMiddlewareDenied(t, rr, http.StatusForbidden, "Admin access required")
		})
	}
}

// ── Observability read surface: dedicated capability gate (PR5) ───────

func TestObservabilityRoutes_CapabilityGate(t *testing.T) {
	router, jwtSvc := newTestRouter(t)

	obsRoutes := []string{
		"/api/v1/admin/pipelines/analytics",
		"/api/v1/admin/messages/1/trace",
	}

	// A token carrying observability:read reaches the handlers.
	withCap := adminToken(t, jwtSvc, []string{"observability:read"})
	for _, path := range obsRoutes {
		t.Run("allow "+path, func(t *testing.T) {
			rr := doRequest(router, http.MethodGet, path, withCap, "")
			assertReachedHandler(t, rr)
		})
	}

	// A token WITHOUT it — even one holding pipelines:read — is denied on the
	// observability surface, proving the dedicated capability is what gates it.
	withoutCap := adminToken(t, jwtSvc, []string{"pipelines:read"})
	for _, path := range obsRoutes {
		t.Run("deny "+path, func(t *testing.T) {
			rr := doRequest(router, http.MethodGet, path, withoutCap, "")
			assertMiddlewareDenied(t, rr, http.StatusForbidden, "Insufficient permissions")
		})
	}

	// That same pipelines:read token STILL reaches the repointed pipelines/logs
	// read — its gate is unchanged (only its data source moved to message_traces).
	t.Run("pipelines:read still reaches pipelines/logs", func(t *testing.T) {
		rr := doRequest(router, http.MethodGet, "/api/v1/admin/pipelines/logs", withoutCap, "")
		assertReachedHandler(t, rr)
	})

	// Superadmin wildcard reaches the observability surface.
	super := adminToken(t, jwtSvc, []string{"*"})
	for _, path := range obsRoutes {
		t.Run("superadmin "+path, func(t *testing.T) {
			rr := doRequest(router, http.MethodGet, path, super, "")
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

// The router harness runs against a deliberately-failing DB, so the OSI-10
// rotation ledger is unreachable here. Refresh is now ledger-gated: it must fail
// CLOSED (401) rather than mint tokens without checking rotation/revocation, and
// the route must still be wired (not a chi 404). The happy-path rotation,
// revocation, and session-type-preservation behavior is covered DB-free by the
// handler tests (handlers.TestRefresh_RotationInvalidatesOldToken,
// TestRefresh_RevokedTokenBlocked, TestRefresh_PreservesTypeWithRotation) and by
// handlers.TestRefresh_Preserves{Admin,Mailbox}Session.

func TestRefresh_MailboxTokenFailsClosedWithoutLedger(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	pair, err := jwtSvc.GenerateTokenPair(10, "user@example.com", 1)
	if err != nil {
		t.Fatalf("failed to generate mailbox pair: %v", err)
	}

	rr := refreshWithCookie(router, pair.RefreshToken)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 fail-closed, got %d (body %s)", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "404 page not found") {
		t.Fatal("refresh route not wired (chi 404)")
	}
}

func TestRefresh_AdminTokenFailsClosedWithoutLedger(t *testing.T) {
	router, jwtSvc := newTestRouter(t)
	pair, err := jwtSvc.GenerateAdminTokenPair(1, "admin", []string{"domains:read", "queue:read"})
	if err != nil {
		t.Fatalf("failed to generate admin pair: %v", err)
	}

	rr := refreshWithCookie(router, pair.RefreshToken)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 fail-closed, got %d (body %s)", rr.Code, rr.Body.String())
	}
}

// ── M-14: pipeline/filter test endpoints are rate-limited ─────────────

// TestPipelineTestRoutes_RateLimited proves the admin pipeline/filter test
// endpoints are throttled per client IP: once the burst is spent, further
// requests get 429 with a Retry-After header. Each of these endpoints runs a
// message through the pipeline engine, so an authenticated admin could
// otherwise hammer them without bound (CWE-770).
func TestPipelineTestRoutes_RateLimited(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins:           []string{"http://localhost:3000"},
		Environment:                  "test",
		PipelineTestRateLimitEnabled: true,
		// Tiny refill so the bucket does not top up mid-test; small burst so the
		// throttle trips quickly.
		PipelineTestRateLimitRPS:   0.0001,
		PipelineTestRateLimitBurst: 3,
	}
	token := adminToken(t, jwtSvc, []string{"*"})

	for _, path := range []string{
		"/api/v1/admin/pipelines/test",
		"/api/v1/admin/pipelines/test-filter",
		"/api/v1/admin/custom-filters/1/test",
	} {
		t.Run(path, func(t *testing.T) {
			// The limiter is shared per-router, so exercise one path per subtest
			// against its own router to keep the burst accounting clean.
			router := NewRouter(newFailingGormDB(t), jwtSvc, cfg, nil)

			passes := 0
			got429 := false
			for i := 0; i < 12; i++ {
				rr := doRequest(router, http.MethodPost, path, token, `{}`)
				if rr.Code == http.StatusTooManyRequests {
					got429 = true
					if rr.Header().Get("Retry-After") == "" {
						t.Errorf("429 response missing Retry-After header")
					}
					break
				}
				// Anything that is not a 429 means the throttle admitted the
				// request to the handler (which then 400/500s on the failing DB).
				assertReachedHandler(t, rr)
				passes++
			}
			if !got429 {
				t.Fatalf("expected a 429 after exhausting the burst, never got one")
			}
			if passes < cfg.PipelineTestRateLimitBurst {
				t.Errorf("throttle tripped too early: %d passes before 429, want >= burst (%d)", passes, cfg.PipelineTestRateLimitBurst)
			}
		})
	}
}

// TestPipelineTestRoutes_ThrottleDisabled is the regression guard: with the
// limiter disabled (the zero-value config the other router tests use), the same
// endpoints never 429 no matter how hard they are hit.
func TestPipelineTestRoutes_ThrottleDisabled(t *testing.T) {
	router, jwtSvc := newTestRouter(t) // PipelineTestRateLimitEnabled defaults false
	token := adminToken(t, jwtSvc, []string{"*"})

	for i := 0; i < 30; i++ {
		rr := doRequest(router, http.MethodPost, "/api/v1/admin/pipelines/test", token, `{}`)
		if rr.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d got 429 with throttle disabled", i)
		}
	}
}

// ── Password-verification oracles: test-connection + LinkAccount ──────

// verifyOracleRoutes are the two authenticated endpoints that verify a supplied
// address+password against a mailbox and reveal correctness (200 vs 401). Both
// must sit behind the same per-client-IP auth throttle as /auth/login, so a
// holder of a valid low-privilege token cannot use them as an unthrottled
// online password-guessing oracle that bypasses the login rate limit.
var verifyOracleRoutes = []struct{ name, path string }{
	{"test-connection", "/api/v1/accounts/test-connection"},
	{"link-account", "/api/v1/accounts"},
}

// TestVerifyOracleRoutes_RateLimited proves that repeated wrong-password
// attempts against test-connection and LinkAccount are throttled: once the
// per-IP burst is spent, further attempts get 429 with a Retry-After header,
// exactly like /auth/login. Without the throttle these routes process every
// guess at full speed.
func TestVerifyOracleRoutes_RateLimited(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins:   []string{"http://localhost:3000"},
		Environment:          "test",
		AuthRateLimitEnabled: true,
		// Tiny refill so the bucket does not top up mid-test; small burst so the
		// throttle trips quickly.
		AuthRateLimitRPS:   0.0001,
		AuthRateLimitBurst: 3,
	}
	token := mailboxToken(t, jwtSvc)
	const wrongPassword = `{"address":"victim@example.com","password":"guess"}`

	for _, rt := range verifyOracleRoutes {
		t.Run(rt.name, func(t *testing.T) {
			// The limiter is shared per-router, so exercise one route per subtest
			// against its own router to keep the burst accounting clean.
			router := NewRouter(newFailingGormDB(t), jwtSvc, cfg, nil)

			passes := 0
			got429 := false
			for i := 0; i < 12; i++ {
				rr := doRequest(router, http.MethodPost, rt.path, token, wrongPassword)
				if rr.Code == http.StatusTooManyRequests {
					got429 = true
					if rr.Header().Get("Retry-After") == "" {
						t.Errorf("429 response missing Retry-After header")
					}
					break
				}
				// A non-429 means the throttle admitted the guess to the handler
				// (which then 401s on the wrong password / failing DB).
				if rr.Code == http.StatusUnauthorized && errorMessage(t, rr) == "Authentication required" {
					t.Fatalf("request rejected by auth middleware, expected to reach handler")
				}
				passes++
			}
			if !got429 {
				t.Fatalf("expected a 429 after exhausting the burst, never got one")
			}
			if passes < cfg.AuthRateLimitBurst {
				t.Errorf("throttle tripped too early: %d passes before 429, want >= burst (%d)", passes, cfg.AuthRateLimitBurst)
			}
		})
	}
}

// TestVerifyOracleRoutes_ThrottleDisabled is the regression guard: with the auth
// limiter disabled, the verify-oracle routes never 429 no matter how hard they
// are hit.
func TestVerifyOracleRoutes_ThrottleDisabled(t *testing.T) {
	router, jwtSvc := newTestRouter(t) // AuthRateLimitEnabled defaults false
	token := mailboxToken(t, jwtSvc)
	const wrongPassword = `{"address":"victim@example.com","password":"guess"}`

	for _, rt := range verifyOracleRoutes {
		t.Run(rt.name, func(t *testing.T) {
			for i := 0; i < 30; i++ {
				rr := doRequest(router, http.MethodPost, rt.path, token, wrongPassword)
				if rr.Code == http.StatusTooManyRequests {
					t.Fatalf("request %d got 429 with throttle disabled", i)
				}
			}
		})
	}
}

// ── #204: 2FA management endpoints share the auth throttle ────────────

// twoFactorThrottledRoutes are the state-changing / code-verifying 2FA
// management endpoints. Left unthrottled they were bounded only by the 15-minute
// access-token lifetime, inconsistent with the throttled login path — an
// attacker with a live session could hammer confirm/disable to brute-force TOTP
// codes. They must sit behind the SAME per-client-IP auth throttle as
// /auth/login.
var twoFactorThrottledRoutes = []struct{ name, path string }{
	{"enroll", "/api/v1/auth/2fa/enroll"},
	{"confirm", "/api/v1/auth/2fa/confirm"},
	{"disable", "/api/v1/auth/2fa/disable"},
}

// TestTwoFactorRoutes_RateLimited proves the 2FA enroll/confirm/disable
// endpoints are throttled per client IP: once the burst is spent, further
// requests get 429 with a Retry-After header, exactly like /auth/login.
func TestTwoFactorRoutes_RateLimited(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins:   []string{"http://localhost:3000"},
		Environment:          "test",
		AuthRateLimitEnabled: true,
		// Tiny refill so the bucket does not top up mid-test; small burst so the
		// throttle trips quickly.
		AuthRateLimitRPS:   0.0001,
		AuthRateLimitBurst: 3,
		// Turn the feature on with a master key so enroll/confirm run past their
		// guard clauses; the specific handler status is irrelevant here — only
		// whether the throttle admitted the request (non-429) or not (429).
		TOTP2FAEnabled: true,
		MasterKey:      "test-master-key-0123456789abcdef",
	}
	token := mailboxToken(t, jwtSvc)

	for _, rt := range twoFactorThrottledRoutes {
		t.Run(rt.name, func(t *testing.T) {
			// The limiter is shared per-router, so exercise one route per subtest
			// against its own router to keep the burst accounting clean.
			router := NewRouter(newFailingGormDB(t), jwtSvc, cfg, nil)

			passes := 0
			got429 := false
			for i := 0; i < 12; i++ {
				rr := doRequest(router, http.MethodPost, rt.path, token, `{"code":"000000"}`)
				if rr.Code == http.StatusTooManyRequests {
					got429 = true
					if rr.Header().Get("Retry-After") == "" {
						t.Errorf("429 response missing Retry-After header")
					}
					break
				}
				// A non-429 means the throttle admitted the request to the handler
				// (which then answers 4xx/5xx); the auth middleware must not have
				// rejected our valid token.
				if rr.Code == http.StatusUnauthorized && errorMessage(t, rr) == "Missing authorization header" {
					t.Fatalf("request rejected by auth middleware, expected to reach handler")
				}
				passes++
			}
			if !got429 {
				t.Fatalf("expected a 429 after exhausting the burst, never got one")
			}
			if passes < cfg.AuthRateLimitBurst {
				t.Errorf("throttle tripped too early: %d passes before 429, want >= burst (%d)", passes, cfg.AuthRateLimitBurst)
			}
		})
	}
}

// TestTwoFactorRoutes_ThrottleDisabled is the regression guard: with the auth
// limiter disabled, the 2FA endpoints never 429 no matter how hard they are hit.
func TestTwoFactorRoutes_ThrottleDisabled(t *testing.T) {
	router, jwtSvc := newTestRouter(t) // AuthRateLimitEnabled defaults false
	token := mailboxToken(t, jwtSvc)

	for _, rt := range twoFactorThrottledRoutes {
		t.Run(rt.name, func(t *testing.T) {
			for i := 0; i < 30; i++ {
				rr := doRequest(router, http.MethodPost, rt.path, token, `{"code":"000000"}`)
				if rr.Code == http.StatusTooManyRequests {
					t.Fatalf("request %d got 429 with throttle disabled", i)
				}
			}
		})
	}
}

// ── OSI-11: security headers wired on the router ──────────────────────

func TestSecurityHeaders_WiredOnRouter(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins:     []string{"http://localhost:3000"},
		Environment:            "test",
		SecurityHeadersEnabled: true,
		HSTSMaxAgeSeconds:      63072000,
	}
	router := NewRouter(newFailingGormDB(t), jwtSvc, cfg, nil)

	// A no-auth endpoint is enough to see the global middleware's headers.
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	h := rr.Result().Header
	if h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", h.Get("X-Frame-Options"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", h.Get("X-Content-Type-Options"))
	}
	if !strings.Contains(h.Get("Strict-Transport-Security"), "max-age=63072000") {
		t.Errorf("Strict-Transport-Security = %q, want max-age=63072000", h.Get("Strict-Transport-Security"))
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("Content-Security-Policy = %q, want strict API policy", h.Get("Content-Security-Policy"))
	}
}

func TestSecurityHeaders_DisabledByConfig(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{
		CORSAllowedOrigins:     []string{"http://localhost:3000"},
		Environment:            "test",
		SecurityHeadersEnabled: false,
	}
	router := NewRouter(newFailingGormDB(t), jwtSvc, cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if got := rr.Result().Header.Get("X-Frame-Options"); got != "" {
		t.Errorf("expected no security headers when disabled, got X-Frame-Options=%q", got)
	}
}
