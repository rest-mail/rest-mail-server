package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/grants"
)

// APIKeyVerify checks an API key and returns the claims its request runs
// with: its account's, with CredentialID, CredentialKind api_key and
// APIKeyPermissions set (CredentialRepository.APIKeyVerifier, in production).
// A key it refuses is an *auth.CredentialRefusal; any other error means it
// could not be checked.
type APIKeyVerify func(key string) (*auth.Claims, error)

// APIKeyRefusedFunc records a refused API key.
type APIKeyRefusedFunc func(r *http.Request, refusal *auth.CredentialRefusal)

// SessionOnly guards changes to a person's own eligible permissions. A
// machine key never becomes a password-authenticated session, even when it
// can perform other explicitly delegated administration.
func SessionOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r)
		if claims == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
			return
		}
		if claims.CredentialID != 0 {
			writeError(w, http.StatusForbidden, "session_required", "Sign in with your password to manage eligible permissions")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// APIKeys lets the routes it guards be called with an API key (#341), sent as
// Authorization: Bearer rmk_..., as well as however the routes were reached
// before. A bearer token with the API key prefix is only ever checked as an
// API key: one that is refused is answered 401, and the request is never
// tried with the session cookie it may also carry. Anything else goes to the
// fallback untouched.
//
// The key is checked on every request, so one that is revoked or expires, or
// whose account is disabled, stops working at the next request. Every refusal
// is recorded, and refusals are throttled as BasicCredentials throttles them:
// a guess is charged to the client's IP, a refusal of a key whose secret
// matched to that key, and requests that succeed cost nothing.
type APIKeys struct {
	verify   APIKeyVerify
	refused  APIKeyRefusedFunc
	failures *rateLimiter // nil: refusals are not throttled
}

// NewAPIKeys builds the API key check. failures sets the refusal budget, per
// client IP and per key; nil leaves refusals unthrottled.
func NewAPIKeys(verify APIKeyVerify, refused APIKeyRefusedFunc, failures *RateLimitConfig) *APIKeys {
	a := &APIKeys{verify: verify, refused: refused}
	if failures != nil {
		a.failures = newRateLimiter(*failures)
	}
	return a
}

// apiKeyFromRequest returns the API key a request is made with, if its
// Authorization header is a bearer token with the API key prefix.
func apiKeyFromRequest(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || !auth.LooksLikeAPIKey(token) {
		return "", false
	}
	return token, true
}

// Authenticate is fallback (JWTMiddleware, or BasicCredentials.Authenticate)
// that also takes an API key.
func (a *APIKeys) Authenticate(fallback func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		other := fallback(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := apiKeyFromRequest(r)
			if !ok {
				other.ServeHTTP(w, r)
				return
			}

			ipKey := "ip:" + clientIP(r)
			if a.failures != nil {
				if ok, retry := a.failures.available(ipKey); !ok {
					writeRateLimited(w, retry)
					return
				}
			}

			claims, err := a.verify(key)
			var refusal *auth.CredentialRefusal
			switch {
			case errors.As(err, &refusal):
				if a.failures != nil {
					limit := ipKey
					if refusal.CredentialID != nil {
						limit = "cred:" + strconv.FormatUint(uint64(*refusal.CredentialID), 10)
					}
					if ok, retry := a.failures.allow(limit); !ok {
						writeRateLimited(w, retry)
						return
					}
				}
				if a.refused != nil {
					a.refused(r, refusal)
				}
				writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired API key")
				return
			case err != nil:
				slog.Error("could not check an API key", "error", err)
				writeError(w, http.StatusServiceUnavailable, "unavailable", "Could not verify the credential")
				return
			case claims == nil || !claims.IsAPIKey() || claims.TwoFactor:
				slog.Error("API key verifier returned claims that are not an API key's")
				writeError(w, http.StatusServiceUnavailable, "unavailable", "Could not verify the credential")
				return
			}

			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CredentialAllows reports whether the credential a request was made with
// lets it use capability at scope (as grants.ParseScope returns it). Only an
// API key limits this: it allows exactly the capabilities its permissions
// name, each at scopes its permission's scope covers, and nothing at all when
// it has none. Every other request is not limited by its credential here
// (true): a session reaches what its account's grants reach, and what an app
// password reaches is decided by Claims.MailboxOnly.
//
// It never grants anything: what the account holds must allow the same
// capability at the same scope too.
func CredentialAllows(claims *auth.Claims, capability, scope string) bool {
	if claims == nil {
		return false
	}
	if !claims.IsScopedCredential() {
		return true
	}
	for _, p := range claims.APIKeyPermissions {
		if p.Capability == capability && grants.Covers(p.Scope, scope) &&
			(claims.CredentialKind != auth.CredentialKindClientSecret || auth.ClientSecretPermissionAllowed(p)) {
			return true
		}
	}
	return false
}

// credentialAllowsSomewhere reports whether the request's credential lets it
// use capability at any scope.
func credentialAllowsSomewhere(claims *auth.Claims, capability string) bool {
	if !claims.IsScopedCredential() {
		return true
	}
	for _, p := range claims.APIKeyPermissions {
		if p.Capability == capability {
			return true
		}
	}
	return false
}
