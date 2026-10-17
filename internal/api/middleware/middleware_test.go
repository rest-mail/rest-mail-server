package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/grants"
)

// signRawToken signs an arbitrary claim set with the given HS256 secret. Used to
// forge tokens the JWTService would never mint — notably a mailbox token still
// carrying a stale is_admin payload — to prove the middleware ignores it
// (OSI-14).
func signRawToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("failed to sign raw token: %v", err)
	}
	return s
}

// newTestJWTService creates a JWTService with a test secret and short expiry durations.
func newTestJWTService(accessExpiry time.Duration) *auth.JWTService {
	return auth.NewJWTService("test-secret-key-for-middleware", accessExpiry, 24*time.Hour)
}

// okHandler is a simple handler that writes 200 OK; used as the "next" handler in middleware chains.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

// parseErrorResponse decodes the standard JSON error body from a response recorder.
func parseErrorResponse(t *testing.T, rr *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var errResp ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	return errResp
}

func TestJWTMiddleware_ValidToken(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	pair, err := jwtSvc.GenerateSession(42, "user@example.com", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatalf("failed to generate token pair: %v", err)
	}

	// Use a handler that captures the claims from the context.
	var capturedClaims *auth.Claims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedClaims = GetClaims(r)
		w.WriteHeader(http.StatusOK)
	})

	handler := JWTMiddleware(jwtSvc)(inner)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if capturedClaims == nil {
		t.Fatal("expected claims in context, got nil")
	}
	if capturedClaims.Address != "user@example.com" {
		t.Errorf("expected address %q, got %q", "user@example.com", capturedClaims.Address)
	}
	if capturedClaims.AccountID != 42 {
		t.Errorf("expected AccountID 42, got %d", capturedClaims.AccountID)
	}
}

func TestJWTMiddleware_MissingHeader(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	handler := JWTMiddleware(jwtSvc)(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	// No Authorization header set.
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}

	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Code != "unauthorized" {
		t.Errorf("expected error code %q, got %q", "unauthorized", errResp.Error.Code)
	}
	if errResp.Error.Message != "Missing authorization header" {
		t.Errorf("expected message %q, got %q", "Missing authorization header", errResp.Error.Message)
	}
}

func TestJWTMiddleware_BadFormat(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	handler := JWTMiddleware(jwtSvc)(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}

	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Code != "unauthorized" {
		t.Errorf("expected error code %q, got %q", "unauthorized", errResp.Error.Code)
	}
	if errResp.Error.Message != "Invalid authorization header format" {
		t.Errorf("expected message %q, got %q", "Invalid authorization header format", errResp.Error.Message)
	}
}

func TestJWTMiddleware_InvalidToken(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	handler := JWTMiddleware(jwtSvc)(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}

	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Code != "unauthorized" {
		t.Errorf("expected error code %q, got %q", "unauthorized", errResp.Error.Code)
	}
	if errResp.Error.Message != "Invalid or expired token" {
		t.Errorf("expected message %q, got %q", "Invalid or expired token", errResp.Error.Message)
	}
}

// TestJWTMiddleware_TokenWithoutSessionIDRejected: every session has a sid,
// so a well-signed access token without one is refused like any invalid token.
func TestJWTMiddleware_TokenWithoutSessionIDRejected(t *testing.T) {
	const secret = "test-secret-key-for-middleware"
	jwtSvc := auth.NewJWTService(secret, 5*time.Minute, 24*time.Hour)
	raw := signRawToken(t, secret, jwt.MapClaims{
		"sub":        "account:10",
		"iss":        auth.TokenIssuer,
		"exp":        time.Now().Add(5 * time.Minute).Unix(),
		"token_type": "access",
		"account_id": 10,
		"address":    "user@example.com",
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rr := httptest.NewRecorder()
	JWTMiddleware(jwtSvc)(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("a token without a sid: expected status 401, got %d", rr.Code)
	}
	if msg := parseErrorResponse(t, rr).Error.Message; msg != "Invalid or expired token" {
		t.Errorf("expected message %q, got %q", "Invalid or expired token", msg)
	}
}

func TestJWTMiddleware_RefreshTokenRejected(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	pair, err := jwtSvc.GenerateSession(42, "user@example.com", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatalf("failed to generate token pair: %v", err)
	}

	// Using a refresh token as a Bearer token should be rejected by the middleware.
	handler := JWTMiddleware(jwtSvc)(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+pair.RefreshToken)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}

	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Code != "unauthorized" {
		t.Errorf("expected error code %q, got %q", "unauthorized", errResp.Error.Code)
	}
}

func TestJWTMiddleware_ExpiredToken(t *testing.T) {
	// Create a JWTService with a negative access expiry so the token is immediately expired.
	jwtSvc := newTestJWTService(-1 * time.Second)
	pair, err := jwtSvc.GenerateSession(1, "expired@example.com", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatalf("failed to generate token pair: %v", err)
	}

	handler := JWTMiddleware(jwtSvc)(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}

	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Code != "unauthorized" {
		t.Errorf("expected error code %q, got %q", "unauthorized", errResp.Error.Code)
	}
	if errResp.Error.Message != "Invalid or expired token" {
		t.Errorf("expected message %q, got %q", "Invalid or expired token", errResp.Error.Message)
	}
}

func TestGetClaims_NoClaims(t *testing.T) {
	// Build a request with no claims stored in context.
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	claims := GetClaims(req)
	if claims != nil {
		t.Errorf("expected nil claims, got %+v", claims)
	}
}

func TestSetupDone_AccountSetUp(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req = req.WithContext(context.WithValue(req.Context(), ClaimsKey, &auth.Claims{AccountID: 1, Address: "admin@example.com"}))
	rr := httptest.NewRecorder()

	SetupDone(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("expected 200 ok, got %d %q", rr.Code, rr.Body.String())
	}
}

func TestSetupDone_Unauthenticated(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rr := httptest.NewRecorder()

	SetupDone(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
	if code := parseErrorResponse(t, rr).Error.Code; code != "unauthorized" {
		t.Errorf("expected error code unauthorized, got %q", code)
	}
}

// A session whose account is still in setup reaches nothing behind SetupDone,
// whatever its account holds.
func TestSetupDone_SessionInSetupRefused(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req = req.WithContext(context.WithValue(req.Context(), ClaimsKey, &auth.Claims{AccountID: 1, Address: "admin@example.com", SetupPending: true}))
	rr := httptest.NewRecorder()

	SetupDone(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rr.Code)
	}
	if code := parseErrorResponse(t, rr).Error.Code; code != "setup_required" {
		t.Errorf("expected error code setup_required, got %q", code)
	}
}

// requestWithClaims builds a request carrying the given claims in its context.
func requestWithClaims(claims *auth.Claims) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/admin/resource", nil)
	return req.WithContext(context.WithValue(req.Context(), ClaimsKey, claims))
}

// heldBy is a HeldGrants answering from a fixed table of accounts' grants.
func heldBy(table map[uint][]grants.Entry) HeldGrants {
	return func(accountID uint) (grants.Held, error) {
		return grants.HeldOf(table[accountID]...), nil
	}
}

// atAll is the entries for capabilities held at "*".
func atAll(caps ...string) []grants.Entry {
	out := make([]grants.Entry, len(caps))
	for i, c := range caps {
		out[i] = grants.Entry{Scope: grants.All, Capability: c}
	}
	return out
}

func TestRequireCapability_AdminWithCapability(t *testing.T) {
	claims := &auth.Claims{
		AccountID: 1,
		Address:   "ops@example.com",
	}
	held := heldBy(map[uint][]grants.Entry{1: atAll("domains:read", "queue:read")})
	rr := httptest.NewRecorder()

	RequireCapability(held, "queue:read", nil)(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
}

func TestRequireCapability_AdminWithWildcard(t *testing.T) {
	claims := &auth.Claims{
		AccountID: 1,
		Address:   "root@example.com",
	}
	held := heldBy(map[uint][]grants.Entry{1: atAll("*")})
	rr := httptest.NewRecorder()

	RequireCapability(held, "users:delete", nil)(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
}

func TestRequireCapability_AdminMissingCapability(t *testing.T) {
	claims := &auth.Claims{
		AccountID: 2,
		Address:   "viewer@example.com",
	}
	held := heldBy(map[uint][]grants.Entry{2: atAll("domains:read", "mailboxes:read")})
	rr := httptest.NewRecorder()

	RequireCapability(held, "domains:delete", nil)(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rr.Code)
	}
	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Message != "Insufficient permissions" {
		t.Errorf("expected message %q, got %q", "Insufficient permissions", errResp.Error.Message)
	}
}

// A token that says it holds everything holds nothing: capabilities come from
// the grants alone.
func TestRequireCapability_TokenCapabilitiesIgnored(t *testing.T) {
	const secret = "test-secret-key-for-middleware"
	jwtSvc := auth.NewJWTService(secret, 5*time.Minute, 24*time.Hour)
	raw := signRawToken(t, secret, jwt.MapClaims{
		"sub":          "account:7",
		"iss":          auth.TokenIssuer,
		"exp":          time.Now().Add(5 * time.Minute).Unix(),
		"token_type":   "access",
		"account_id":   7,
		"address":      "ops@example.com",
		"capabilities": []string{"*"},
		"sid":          "0123456789abcdef0123456789abcdef",
	})
	held := heldBy(map[uint][]grants.Entry{7: atAll("domains:read")})

	handler := JWTMiddleware(jwtSvc)(RequireCapability(held, "users:delete", nil)(okHandler))
	req := httptest.NewRequest(http.MethodGet, "/admin/resource", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("a token claiming capabilities its grants lack: expected 403, got %d", rr.Code)
	}
}

// scopeIs is a ScopeOf naming one fixed scope.
func scopeIs(scope string) ScopeOf {
	return func(*http.Request) (string, bool, error) { return scope, true, nil }
}

func TestRequireCapability_ScopedGrantCoversItsTargetOnly(t *testing.T) {
	claims := &auth.Claims{AccountID: 5, Address: "a-admin@example.com"}
	held := heldBy(map[uint][]grants.Entry{5: {
		{Scope: "domain:a.com", Capability: "domains:read"},
		{Scope: "domain:a.com", Capability: "mailboxes:read"},
	}})

	cases := []struct {
		name       string
		capability string
		target     ScopeOf
		want       int
	}{
		{"its own domain", "domains:read", scopeIs("domain:a.com"), http.StatusOK},
		{"a mailbox of its domain", "mailboxes:read", scopeIs("mailbox:x@a.com"), http.StatusOK},
		{"another domain", "domains:read", scopeIs("domain:b.com"), http.StatusForbidden},
		{"a mailbox of another domain", "mailboxes:read", scopeIs("mailbox:x@b.com"), http.StatusForbidden},
		{"a capability it lacks there", "domains:write", scopeIs("domain:a.com"), http.StatusForbidden},
		{"a route that wants *", "domains:read", nil, http.StatusForbidden},
		{"a target that does not exist", "domains:read", func(*http.Request) (string, bool, error) { return "", false, nil }, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			RequireCapability(held, tc.capability, tc.target)(okHandler).ServeHTTP(rr, requestWithClaims(claims))
			if rr.Code != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, rr.Code)
			}
		})
	}
}

// Holding a capability at "*" needs no lookup of the target, so a target that
// does not exist reaches the handler (and its 404) exactly as before grants.
func TestRequireCapability_AtAllSkipsTheTargetLookup(t *testing.T) {
	claims := &auth.Claims{AccountID: 1, Address: "ops@example.com"}
	held := heldBy(map[uint][]grants.Entry{1: atAll("domains:read")})
	target := func(*http.Request) (string, bool, error) {
		t.Fatal("looked the target up for a grant at *")
		return "", false, nil
	}
	rr := httptest.NewRecorder()
	RequireCapability(held, "domains:read", target)(okHandler).ServeHTTP(rr, requestWithClaims(claims))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
}

// Failing to read the grants refuses the request: it never falls open.
func TestRequireCapability_UnreadableGrantsRefused(t *testing.T) {
	claims := &auth.Claims{AccountID: 1, Address: "ops@example.com"}
	held := func(uint) (grants.Held, error) { return grants.Held{}, errors.New("database down") }
	rr := httptest.NewRecorder()
	RequireCapability(held, "domains:read", nil)(okHandler).ServeHTTP(rr, requestWithClaims(claims))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rr.Code)
	}
}

// An account holding no admin role is refused on an admin route: every
// account can sign in, and only grants make one an admin.
func TestRequireCapability_AccountWithoutGrantsDenied(t *testing.T) {
	claims := &auth.Claims{AccountID: 10, Address: "user@example.com"}
	rr := httptest.NewRecorder()

	RequireCapability(heldBy(nil), "domains:read", nil)(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rr.Code)
	}
	if msg := parseErrorResponse(t, rr).Error.Message; msg != "Insufficient permissions" {
		t.Errorf("expected message %q, got %q", "Insufficient permissions", msg)
	}
}

// A request made with a credential over HTTP Basic is refused on an admin
// route, whatever its account holds: administration needs a session.
func TestRequireCapability_BasicCredentialRefused(t *testing.T) {
	claims := &auth.Claims{AccountID: 1, Address: "root@example.com", CredentialID: 9, CredentialKind: "password"}
	held := heldBy(map[uint][]grants.Entry{1: atAll("*")})
	rr := httptest.NewRecorder()

	RequireCapability(held, "domains:read", nil)(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rr.Code)
	}
}

// A token with claims of its own making (is_admin, user_type) holds nothing
// for them: capabilities come from the grants alone.
func TestRequireCapability_StrayClaimsGrantNothing(t *testing.T) {
	const secret = "test-secret-key-for-middleware"
	jwtSvc := auth.NewJWTService(secret, 5*time.Minute, 24*time.Hour)
	raw := signRawToken(t, secret, jwt.MapClaims{
		"sub":        "account:10",
		"iss":        "restmail",
		"exp":        time.Now().Add(5 * time.Minute).Unix(),
		"user_type":  "admin",
		"token_type": "access",
		"is_admin":   true,
		"account_id": 10,
		"sid":        "0123456789abcdef0123456789abcdef",
	})

	handler := JWTMiddleware(jwtSvc)(RequireCapability(heldBy(nil), "queue:manage", nil)(okHandler))
	req := httptest.NewRequest(http.MethodGet, "/admin/resource", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rr.Code)
	}
}

func TestRequireCapability_NoClaims(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/resource", nil)
	rr := httptest.NewRecorder()

	RequireCapability(heldBy(nil), "domains:read", nil)(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Message != "Authentication required" {
		t.Errorf("expected message %q, got %q", "Authentication required", errResp.Error.Message)
	}
}

func TestRequireCapability_EmptyCapabilityListDenied(t *testing.T) {
	claims := &auth.Claims{
		AccountID: 3,
		Address:   "no-caps@example.com",
	}
	rr := httptest.NewRecorder()

	RequireCapability(heldBy(nil), "bans:read", nil)(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rr.Code)
	}
}
