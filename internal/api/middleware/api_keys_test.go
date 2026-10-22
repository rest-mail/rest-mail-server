package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/grants"
)

const testAPIKey = "rmk_AAAAAAAAAAAA_secretsecretsecretsecretsecretsecretsecretsec"

func apiKeyClaims(perms ...auth.ScopedPermission) *auth.Claims {
	return &auth.Claims{AccountID: 7, Address: "bot@a.com", TokenType: "access", CredentialID: 9,
		CredentialKind: auth.CredentialKindAPIKey, APIKeyPermissions: perms}
}

func contextOf(r *http.Request, c *auth.Claims) context.Context {
	return context.WithValue(r.Context(), ClaimsKey, c)
}

func TestAPIKeys_Authenticate(t *testing.T) {
	var refused []*auth.CredentialRefusal
	verify := func(key string) (*auth.Claims, error) {
		switch key {
		case testAPIKey:
			return apiKeyClaims(auth.ScopedPermission{Capability: "mailboxes:write", Scope: "domain:a.com"}), nil
		case "rmk_broken":
			return nil, &auth.CredentialRefusal{Reason: auth.CredentialMalformed}
		case "rmk_down":
			return nil, errors.New("database down")
		}
		id := uint(9)
		return nil, &auth.CredentialRefusal{Reason: auth.CredentialRevoked, CredentialID: &id}
	}
	keys := NewAPIKeys(verify, func(_ *http.Request, r *auth.CredentialRefusal) { refused = append(refused, r) }, nil)

	var got *auth.Claims
	h := keys.Authenticate(JWTMiddleware(auth.NewJWTService("secret", 0, 0)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = GetClaims(r)
	}))

	cases := []struct {
		name   string
		header string
		cookie bool
		status int
		key    bool
	}{
		{"valid key", "Bearer " + testAPIKey, false, http.StatusOK, true},
		{"lower-case scheme", "bearer " + testAPIKey, false, http.StatusOK, true},
		{"revoked key with a session cookie", "Bearer rmk_revokedrevokedrevoked", true, http.StatusUnauthorized, false},
		{"malformed key", "Bearer rmk_broken", true, http.StatusUnauthorized, false},
		{"unavailable", "Bearer rmk_down", false, http.StatusServiceUnavailable, false},
		{"not a key: the session path", "Bearer not-a-jwt", false, http.StatusUnauthorized, false},
		{"key sent with Basic is not a key", "Basic Ym90QGEuY29tOnJta19BQUFB", false, http.StatusUnauthorized, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got = nil
			req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
			req.Header.Set("Authorization", tc.header)
			if tc.cookie {
				req.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: "a-session"})
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.key {
				if got == nil || !got.IsAPIKey() || got.CredentialID != 9 || got.TwoFactor {
					t.Fatalf("claims = %+v, want the key's", got)
				}
			} else if got != nil {
				t.Fatalf("handler reached with %+v", got)
			}
		})
	}
	if len(refused) != 2 {
		t.Fatalf("recorded %d refusals, want 2 (revoked, malformed)", len(refused))
	}
}

func TestAPIKeys_OtherRequestsGoToTheFallback(t *testing.T) {
	keys := NewAPIKeys(func(string) (*auth.Claims, error) {
		t.Fatal("verify called for a request without an API key")
		return nil, nil
	}, nil, nil)
	h := keys.Authenticate(fallbackMarkerMiddleware)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, header := range []string{"", "Bearer eyJ.a.b", "Basic Ym90QGEuY29tOnBhc3N3b3Jk"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("X-Fallback") != "1" {
			t.Fatalf("%q did not go to the fallback", header)
		}
	}
}

func fallbackMarkerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fallback", "1")
		next.ServeHTTP(w, r)
	})
}

func TestAPIKeys_RefusalsAreThrottled(t *testing.T) {
	keys := NewAPIKeys(func(string) (*auth.Claims, error) {
		return nil, &auth.CredentialRefusal{Reason: auth.CredentialUnknown}
	}, nil, &RateLimitConfig{RPS: 0.001, Burst: 1})
	h := keys.Authenticate(fallbackMarkerMiddleware)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	codes := []int{}
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+testAPIKey)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[0] != http.StatusUnauthorized || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v, want 401 then 429", codes)
	}
}

func TestCredentialAllows(t *testing.T) {
	key := apiKeyClaims(
		auth.ScopedPermission{Capability: "mailboxes:write", Scope: "domain:a.com"},
		auth.ScopedPermission{Capability: "mail:read", Scope: "mailbox:bob@b.com"},
	)
	cases := []struct {
		claims     *auth.Claims
		capability string
		scope      string
		want       bool
	}{
		{key, "mailboxes:write", "domain:a.com", true},
		{key, "mailboxes:write", "mailbox:x@a.com", true},
		{key, "mailboxes:write", "domain:b.com", false},
		{key, "mailboxes:write", grants.All, false},
		{key, "mailboxes:read", "domain:a.com", false},
		{key, "mail:read", "mailbox:bob@b.com", true},
		{key, "mail:read", "domain:b.com", false},
		{apiKeyClaims(), "mailboxes:write", "domain:a.com", false},
		{apiKeyClaims(auth.ScopedPermission{Capability: "*", Scope: "*"}), "mailboxes:write", "domain:a.com", false},
		{&auth.Claims{AccountID: 1, SessionID: "s"}, "mailboxes:write", grants.All, true},
		{nil, "mailboxes:write", grants.All, false},
	}
	for i, tc := range cases {
		if got := CredentialAllows(tc.claims, tc.capability, tc.scope); got != tc.want {
			t.Errorf("case %d: CredentialAllows(%q, %q) = %v, want %v", i, tc.capability, tc.scope, got, tc.want)
		}
	}
}

func TestRequireCapability_APIKeyNeedsBothGrantAndPermission(t *testing.T) {
	target := func(scope string) ScopeOf {
		return func(*http.Request) (string, bool, error) { return scope, true, nil }
	}
	cases := []struct {
		name   string
		held   grants.Held
		claims *auth.Claims
		target ScopeOf
		want   int
	}{
		{"both at the domain", grants.HeldOf(grants.Entry{Scope: "domain:a.com", Capability: "mailboxes:write"}),
			apiKeyClaims(auth.ScopedPermission{Capability: "mailboxes:write", Scope: "domain:a.com"}), target("domain:a.com"), http.StatusOK},
		{"grant at *, key at the domain", grants.HeldOf(grants.Entry{Scope: "*", Capability: "*"}),
			apiKeyClaims(auth.ScopedPermission{Capability: "mailboxes:write", Scope: "domain:a.com"}), target("domain:a.com"), http.StatusOK},
		{"grant at *, key at the domain, other domain", grants.HeldOf(grants.Entry{Scope: "*", Capability: "*"}),
			apiKeyClaims(auth.ScopedPermission{Capability: "mailboxes:write", Scope: "domain:a.com"}), target("domain:b.com"), http.StatusForbidden},
		{"grant at *, key at the domain, no target", grants.HeldOf(grants.Entry{Scope: "*", Capability: "*"}),
			apiKeyClaims(auth.ScopedPermission{Capability: "mailboxes:write", Scope: "domain:a.com"}), nil, http.StatusForbidden},
		{"grant removed: the key keeps nothing", grants.HeldOf(),
			apiKeyClaims(auth.ScopedPermission{Capability: "mailboxes:write", Scope: "*"}), target("domain:a.com"), http.StatusForbidden},
		{"grant at another domain", grants.HeldOf(grants.Entry{Scope: "domain:b.com", Capability: "mailboxes:write"}),
			apiKeyClaims(auth.ScopedPermission{Capability: "mailboxes:write", Scope: "*"}), target("domain:a.com"), http.StatusForbidden},
		{"key for another capability", grants.HeldOf(grants.Entry{Scope: "*", Capability: "*"}),
			apiKeyClaims(auth.ScopedPermission{Capability: "mailboxes:read", Scope: "*"}), target("domain:a.com"), http.StatusForbidden},
		{"key with no permissions", grants.HeldOf(grants.Entry{Scope: "*", Capability: "*"}),
			apiKeyClaims(), nil, http.StatusForbidden},
		{"both at *", grants.HeldOf(grants.Entry{Scope: "*", Capability: "mailboxes:write"}),
			apiKeyClaims(auth.ScopedPermission{Capability: "mailboxes:write", Scope: "*"}), nil, http.StatusOK},
		{"app password still refused", grants.HeldOf(grants.Entry{Scope: "*", Capability: "*"}),
			&auth.Claims{AccountID: 7, CredentialID: 3, CredentialKind: "app_password"}, nil, http.StatusForbidden},
		{"gateway password still refused", grants.HeldOf(grants.Entry{Scope: "*", Capability: "*"}),
			&auth.Claims{AccountID: 7, CredentialID: 3, CredentialKind: "password"}, nil, http.StatusForbidden},
		{"session unchanged", grants.HeldOf(grants.Entry{Scope: "domain:a.com", Capability: "mailboxes:write"}),
			&auth.Claims{AccountID: 7, SessionID: "s"}, target("domain:a.com"), http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			held := func(uint) (grants.Held, error) { return tc.held, nil }
			h := RequireCapability(held, "mailboxes:write", tc.target)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req = req.WithContext(contextOf(req, tc.claims))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
