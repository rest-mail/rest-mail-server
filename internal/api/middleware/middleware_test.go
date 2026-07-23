package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
)

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
	pair, err := jwtSvc.GenerateTokenPair(42, "user@example.com", 7, false)
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
	if capturedClaims.Email != "user@example.com" {
		t.Errorf("expected email %q, got %q", "user@example.com", capturedClaims.Email)
	}
	if capturedClaims.MailboxID != 42 {
		t.Errorf("expected MailboxID 42, got %d", capturedClaims.MailboxID)
	}
	if capturedClaims.WebmailAccountID != 7 {
		t.Errorf("expected WebmailAccountID 7, got %d", capturedClaims.WebmailAccountID)
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

func TestJWTMiddleware_RefreshTokenRejected(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	pair, err := jwtSvc.GenerateTokenPair(42, "user@example.com", 7, false)
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
	pair, err := jwtSvc.GenerateTokenPair(1, "expired@example.com", 1, false)
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

func TestAdminOnly_Authenticated(t *testing.T) {
	// Simulate an authenticated request by injecting claims into the context.
	claims := &auth.Claims{
		Email:            "admin@example.com",
		WebmailAccountID: 1,
		MailboxID:        10,
		IsAdmin:          true,
	}

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	ctx := context.WithValue(req.Context(), ClaimsKey, claims)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()

	AdminOnly(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
	if rr.Body.String() != "ok" {
		t.Errorf("expected body %q, got %q", "ok", rr.Body.String())
	}
}

func TestAdminOnly_Unauthenticated(t *testing.T) {
	// No claims in context -- should be rejected.
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rr := httptest.NewRecorder()

	AdminOnly(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}

	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Code != "unauthorized" {
		t.Errorf("expected error code %q, got %q", "unauthorized", errResp.Error.Code)
	}
	if errResp.Error.Message != "Authentication required" {
		t.Errorf("expected message %q, got %q", "Authentication required", errResp.Error.Message)
	}
}

func TestAdminOnly_MailboxTokenDenied(t *testing.T) {
	// A plain mailbox token (no admin markers) must be rejected with 403.
	claims := &auth.Claims{
		Email:            "user@example.com",
		WebmailAccountID: 1,
		MailboxID:        10,
		UserType:         "mailbox",
	}

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req = req.WithContext(context.WithValue(req.Context(), ClaimsKey, claims))
	rr := httptest.NewRecorder()

	AdminOnly(okHandler).ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rr.Code)
	}
	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Message != "Admin access required" {
		t.Errorf("expected message %q, got %q", "Admin access required", errResp.Error.Message)
	}
}

// requestWithClaims builds a request carrying the given claims in its context.
func requestWithClaims(claims *auth.Claims) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/admin/resource", nil)
	return req.WithContext(context.WithValue(req.Context(), ClaimsKey, claims))
}

func TestRequireCapability_AdminWithCapability(t *testing.T) {
	claims := &auth.Claims{
		UserType:     "admin",
		AdminUserID:  1,
		Username:     "ops",
		Capabilities: []string{"domains:read", "queue:read"},
	}
	rr := httptest.NewRecorder()

	RequireCapability("queue:read")(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
}

func TestRequireCapability_AdminWithWildcard(t *testing.T) {
	claims := &auth.Claims{
		UserType:     "admin",
		AdminUserID:  1,
		Username:     "root",
		Capabilities: []string{"*"},
	}
	rr := httptest.NewRecorder()

	RequireCapability("users:delete")(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
}

func TestRequireCapability_AdminMissingCapability(t *testing.T) {
	claims := &auth.Claims{
		UserType:     "admin",
		AdminUserID:  2,
		Username:     "viewer",
		Capabilities: []string{"domains:read", "mailboxes:read"},
	}
	rr := httptest.NewRecorder()

	RequireCapability("domains:delete")(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rr.Code)
	}
	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Message != "Insufficient permissions" {
		t.Errorf("expected message %q, got %q", "Insufficient permissions", errResp.Error.Message)
	}
}

func TestRequireCapability_MailboxTokenDenied(t *testing.T) {
	claims := &auth.Claims{
		Email:            "user@example.com",
		WebmailAccountID: 1,
		MailboxID:        10,
		UserType:         "mailbox",
	}
	rr := httptest.NewRecorder()

	RequireCapability("domains:read")(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rr.Code)
	}
	errResp := parseErrorResponse(t, rr)
	if errResp.Error.Message != "Admin access required" {
		t.Errorf("expected message %q, got %q", "Admin access required", errResp.Error.Message)
	}
}

func TestRequireCapability_LegacyIsAdminMailboxAllowed(t *testing.T) {
	// Mailbox tokens carrying the deprecated IsAdmin flag had full admin
	// access under AdminOnly; RequireCapability must preserve that so
	// already-issued tokens keep working.
	claims := &auth.Claims{
		Email:            "legacy-admin@example.com",
		WebmailAccountID: 1,
		MailboxID:        10,
		UserType:         "mailbox",
		IsAdmin:          true,
	}
	rr := httptest.NewRecorder()

	RequireCapability("queue:manage")(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}
}

func TestRequireCapability_NoClaims(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/resource", nil)
	rr := httptest.NewRecorder()

	RequireCapability("domains:read")(okHandler).ServeHTTP(rr, req)

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
		UserType:    "admin",
		AdminUserID: 3,
		Username:    "no-caps",
	}
	rr := httptest.NewRecorder()

	RequireCapability("bans:read")(okHandler).ServeHTTP(rr, requestWithClaims(claims))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rr.Code)
	}
}
