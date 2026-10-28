package middleware

import (
	"context"
	"net/http"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/grants"
)

type listScopesKey struct{}
type authorizedGrantsKey struct{}
type listScopes struct {
	capability string
	scopes     []string
}

// AuthorizedGrants is the live snapshot used to authorize this request.
func AuthorizedGrants(r *http.Request) (grants.Held, bool) {
	h, ok := r.Context().Value(authorizedGrantsKey{}).(grants.Held)
	return h, ok
}

func withAuthorizedGrants(r *http.Request, h grants.Held) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authorizedGrantsKey{}, h))
}

// CapabilityScopes is set only by RequireCapabilityOnList. A handler using
// this must apply it before counting, ordering or paging the database query.
func CapabilityScopes(r *http.Request, capability string) ([]string, bool) {
	s, ok := r.Context().Value(listScopesKey{}).(listScopes)
	if !ok {
		return nil, false
	}
	if s.capability != capability {
		return nil, true
	}
	return append([]string(nil), s.scopes...), true
}

func permittedScopes(h grants.Held, claims *auth.Claims, capability string) []string {
	scopes := h.Scopes(capability)
	if !claims.IsScopedCredential() {
		return scopes
	}
	var keyScopes []string
	for _, p := range claims.APIKeyPermissions {
		if p.Capability == capability && (claims.CredentialKind != auth.CredentialKindClientSecret || auth.ClientSecretPermissionAllowed(p)) {
			keyScopes = append(keyScopes, p.Scope)
		}
	}
	return grants.IntersectScopes(scopes, keyScopes)
}

// RequireCapabilityOnList accepts a capability at any scope, and carries its
// exact authorized scopes to the list handler. API keys receive the intersection
// with their own permissions; disjoint permissions never grant access.
func RequireCapabilityOnList(held HeldGrants, capability string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				writeError(w, 401, "unauthorized", "Authentication required")
				return
			}
			if claims.CredentialID != 0 && !claims.IsScopedCredential() {
				writeError(w, 403, "forbidden", "Sign in to do this")
				return
			}
			h, err := held(claims.AccountID)
			if err != nil {
				writeError(w, 503, "unavailable", "Could not verify permissions")
				return
			}
			scopes := permittedScopes(h, claims, capability)
			if len(scopes) == 0 {
				writeError(w, 403, "forbidden", "Insufficient permissions")
				return
			}
			r = withAuthorizedGrants(r, h)
			r = r.WithContext(context.WithValue(r.Context(), listScopesKey{}, listScopes{capability, scopes}))
			next.ServeHTTP(w, r)
		})
	}
}
