package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/grants"
)

type contextKey string

const ClaimsKey contextKey = "claims"

// AccessTokenFromRequest extracts the JWT access token from a request, accepting
// EITHER transport:
//
//   - the Authorization: Bearer <token> header — used by programmatic/API
//     clients (the protocol gateways, tooling). Checked first and, when present,
//     authoritative: a present-but-malformed header is rejected rather than
//     silently falling through to the cookie.
//   - the restmail_access httpOnly cookie — used by the browser SPAs, which no
//     longer hold the token in JavaScript at all.
//
// It returns the raw token, or a non-empty errMsg describing why none could be
// read (preserving the historical messages the API surfaced for a
// missing/malformed Authorization header).
func AccessTokenFromRequest(r *http.Request) (token, errMsg string) {
	if header := r.Header.Get("Authorization"); header != "" {
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return "", "Invalid authorization header format"
		}
		return parts[1], ""
	}
	if c, err := r.Cookie(auth.AccessCookieName); err == nil && c.Value != "" {
		return c.Value, ""
	}
	return "", "Missing authorization header"
}

// JWTMiddleware validates the access token, read from either the
// Authorization: Bearer header or the restmail_access cookie (see
// AccessTokenFromRequest). Validation is unchanged — only the accepted transport
// widens to include the httpOnly cookie.
func JWTMiddleware(jwtService *auth.JWTService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, errMsg := AccessTokenFromRequest(r)
			if errMsg != "" {
				writeError(w, http.StatusUnauthorized, "unauthorized", errMsg)
				return
			}

			claims, err := jwtService.ValidateAccessToken(token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetClaims extracts JWT claims from the request context.
func GetClaims(r *http.Request) *auth.Claims {
	claims, ok := r.Context().Value(ClaimsKey).(*auth.Claims)
	if !ok {
		return nil
	}
	return claims
}

// SetupDone refuses a session whose account is still in setup (a new
// password, a 2FA enrollment still owed): it reaches nothing behind this,
// including routes that check no capability. The endpoints that finish the
// setup — /api/v1/auth/password and /api/v1/auth/2fa/* — are not behind it.
func SetupDone(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r)
		if claims == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
			return
		}
		if claims.SetupPending {
			writeError(w, http.StatusForbidden, "setup_required", "Finish setting up this account first: a new password and two-factor authentication")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// HeldGrants is what an account holds right now, from its active grants: a
// grants.Cache over the database, in production.
type HeldGrants func(accountID uint) (grants.Held, error)

// ScopeOf finds the scope of the domain or mailbox a request acts on, from
// its URL parameters. found is false when there is no such domain or mailbox.
type ScopeOf func(r *http.Request) (scope string, found bool, err error)

// RequireCapability restricts a route to accounts holding capability, read
// from their grants on every request, never from the token. The wildcard "*"
// capability satisfies every requirement.
//
// With no target, the capability must be held at "*". With a target, a route
// acting on a domain or a mailbox, it may instead be held at a scope covering
// that domain or mailbox: a domain admin on domain:a.com reaches a.com and its
// mailboxes, and is refused on b.com. Someone holding it at "*" is let through
// without looking the target up, so the handler answers for one that does not
// exist exactly as before; anyone else is refused for it.
//
// Only a session is eligible: a request made with a credential over HTTP
// Basic (an app password, a gateway's password) is refused, whatever its
// account holds.
func RequireCapability(held HeldGrants, capability string, target ScopeOf) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
				return
			}
			if claims.CredentialID != 0 {
				writeError(w, http.StatusForbidden, "forbidden", "Sign in to do this")
				return
			}

			h, err := held(claims.AccountID)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "unavailable", "Could not verify permissions")
				return
			}
			if h.Allows(capability, grants.All) {
				next.ServeHTTP(w, r)
				return
			}
			if target != nil && h.AllowsSomewhere(capability) {
				scope, found, err := target(r)
				if err != nil {
					writeError(w, http.StatusServiceUnavailable, "unavailable", "Could not verify permissions")
					return
				}
				if found && h.Allows(capability, scope) {
					next.ServeHTTP(w, r)
					return
				}
			}

			writeError(w, http.StatusForbidden, "forbidden", "Insufficient permissions")
		})
	}
}

// SessionCurrent reports whether an access token minted at credential epoch
// epoch is still good for accountID: the account exists, is active, and has
// not changed its credentials since.
type SessionCurrent func(accountID uint, epoch int) (bool, error)

// RejectStaleSessions refuses access tokens minted before the account last
// changed its credentials (they carry an older credential epoch), or whose
// account is gone or disabled. Access tokens are stateless and live for
// minutes; without this, one taken before a password change or a reset keeps
// working until it expires — long enough, after a reset has wiped the
// authenticator, for its holder to enroll their own and take the account
// back. A request made with a credential over HTTP Basic passes: the
// credential was checked just now.
func RejectStaleSessions(current SessionCurrent) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := GetClaims(r)
			if claims == nil || claims.CredentialID != 0 {
				next.ServeHTTP(w, r)
				return
			}
			ok, err := current(claims.AccountID, claims.CredentialEpoch)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "unavailable", "Could not verify the session")
				return
			}
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized", "This session ended when the account's credentials changed; sign in again")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
