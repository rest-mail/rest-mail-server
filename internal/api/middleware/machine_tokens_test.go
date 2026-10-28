package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/grants"
)

func TestMachineTokenRevocationAndLivePermissions(t *testing.T) {
	tokens := auth.NewJWTService("secret", time.Hour, 24*time.Hour)
	token, err := tokens.GenerateMachineToken(12, "robot@example.test", 34)
	if err != nil {
		t.Fatal(err)
	}
	revoked, granted := false, true
	checks := 0
	verify := func(c *auth.Claims) error {
		checks++
		if revoked {
			return auth.ErrInvalidCredentials
		}
		c.CredentialID = 34
		c.CredentialKind = auth.CredentialKindClientSecret
		if granted {
			c.APIKeyPermissions = []auth.ScopedPermission{{Capability: "pipelines:write", Scope: "domain:example.test"}}
		}
		return nil
	}
	// An intentionally stale account-grant cache must not preserve lost access.
	held := func(uint) (grants.Held, error) {
		return grants.HeldOf(grants.Entry{Capability: "pipelines:write", Scope: "domain:example.test"}), nil
	}
	scope := func(*http.Request) (string, bool, error) { return "domain:example.test", true, nil }
	handler := JWTMiddleware(tokens, verify)(RequireCapability(held, "pipelines:write", scope)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := GetClaims(r)
		if c.TwoFactor || len(c.AMR) != 0 {
			t.Fatal("machine acquired human authentication")
		}
		w.WriteHeader(204)
	})))
	send := func(h http.Handler, want int) {
		req := httptest.NewRequest("POST", "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("got %d %s, want %d", rr.Code, rr.Body, want)
		}
	}
	send(handler, 204)
	granted = false
	send(handler, 403)
	granted = true
	revoked = true
	send(handler, 401)
	if checks != 3 {
		t.Fatalf("credential checked %d times, want 3", checks)
	}
	send(JWTMiddleware(tokens)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("machine entered person-only route") })), 401)
}

func TestClientSecretCredentialScope(t *testing.T) {
	c := &auth.Claims{CredentialID: 34, CredentialKind: auth.CredentialKindClientSecret, APIKeyPermissions: []auth.ScopedPermission{
		{Capability: "pipelines:write", Scope: "domain:example.test"},
		{Capability: "users:write", Scope: "domain:example.test"},
	}}
	if !CredentialAllows(c, "pipelines:write", "domain:example.test") || CredentialAllows(c, "pipelines:write", "domain:other.test") || CredentialAllows(c, "users:write", "domain:example.test") || CredentialAllows(c, "mail:read", "mailbox:robot@example.test") {
		t.Fatal("client-secret tier or scope exceeded")
	}
	if got := permittedScopes(grants.HeldOf(grants.Entry{Capability: "users:write", Scope: "domain:example.test"}), c, "users:write"); len(got) != 0 {
		t.Fatal("list authorization bypassed the tier")
	}
}
