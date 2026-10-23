package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/grants"
)

func TestScopedListAuthorization(t *testing.T) {
	const cap = "mailboxes:read"
	for _, tc := range []struct {
		name    string
		claims  *auth.Claims
		entries []grants.Entry
		err     error
		status  int
		want    []string
	}{
		{name: "anonymous", status: 401},
		{name: "session domain", claims: &auth.Claims{AccountID: 1}, entries: []grants.Entry{{Scope: "domain:a.test", Capability: cap}}, status: 200, want: []string{"domain:a.test"}},
		{name: "global wildcard", claims: &auth.Claims{AccountID: 1}, entries: []grants.Entry{{Scope: "*", Capability: "*"}}, status: 200, want: []string{"*"}},
		{name: "wrong capability", claims: &auth.Claims{AccountID: 1}, entries: []grants.Entry{{Scope: "*", Capability: "queue:read"}}, status: 403},
		{name: "basic admin refused", claims: &auth.Claims{AccountID: 1, CredentialID: 3, CredentialKind: "app_password"}, entries: []grants.Entry{{Scope: "*", Capability: "*"}}, status: 403},
		{name: "global owner scoped key", claims: &auth.Claims{AccountID: 1, CredentialID: 3, CredentialKind: auth.CredentialKindAPIKey, APIKeyPermissions: []auth.ScopedPermission{{Capability: cap, Scope: "domain:a.test"}}}, entries: []grants.Entry{{Scope: "*", Capability: "*"}}, status: 200, want: []string{"domain:a.test"}},
		{name: "disjoint owner and key", claims: &auth.Claims{AccountID: 1, CredentialID: 3, CredentialKind: auth.CredentialKindAPIKey, APIKeyPermissions: []auth.ScopedPermission{{Capability: cap, Scope: "domain:b.test"}}}, entries: []grants.Entry{{Scope: "domain:a.test", Capability: cap}}, status: 403},
		{name: "revoked owner grant", claims: &auth.Claims{AccountID: 1, CredentialID: 3, CredentialKind: auth.CredentialKindAPIKey, APIKeyPermissions: []auth.ScopedPermission{{Capability: cap, Scope: "*"}}}, status: 403},
		{name: "unavailable", claims: &auth.Claims{AccountID: 1}, err: errors.New("database down"), status: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := grants.HeldOf(tc.entries...)
			calls := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				scopes, ok := CapabilityScopes(r, cap)
				if !ok || !reflect.DeepEqual(scopes, tc.want) {
					t.Errorf("scopes=%v,%v want%v", scopes, ok, tc.want)
				}
				if _, ok := AuthorizedGrants(r); !ok {
					t.Error("missing live grants context")
				}
				scopes[0] = "mutated"
				second, _ := CapabilityScopes(r, cap)
				if reflect.DeepEqual(scopes, second) {
					t.Error("context scopes mutable")
				}
				if other, ok := CapabilityScopes(r, "domains:read"); !ok || len(other) > 0 {
					t.Error("scope context crossed capabilities")
				}
				w.WriteHeader(200)
			})
			rr := httptest.NewRecorder()
			RequireCapabilityOnList(func(uint) (grants.Held, error) { return h, tc.err }, cap)(next).ServeHTTP(rr, requestWithClaims(tc.claims))
			if rr.Code != tc.status {
				t.Fatalf("status=%d want%d body%s", rr.Code, tc.status, rr.Body.String())
			}
			if (calls == 1) != (tc.status == 200) {
				t.Fatalf("handler calls=%d", calls)
			}
		})
	}
}
