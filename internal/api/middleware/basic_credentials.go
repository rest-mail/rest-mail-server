package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/restmail/restmail/internal/auth"
)

// BasicVerify checks an address and a password sent with HTTP Basic (an app
// password, or one of the account's passwords), and returns the claims the
// request then runs with: those of the account it belongs to, with
// CredentialID and CredentialKind set (handlers.BasicVerifier, in
// production). A credential it refuses is an *auth.CredentialRefusal; any
// other error means it could not be checked.
type BasicVerify func(address, password string) (*auth.Claims, error)

// BasicRefusedFunc records a refused credential: Auditor.BasicRefused, in
// production.
type BasicRefusedFunc func(r *http.Request, address string, refusal *auth.CredentialRefusal)

// BasicCredentials lets the routes it guards be called with a credential sent
// with HTTP Basic as well as with a session: the account's address as the user
// name, and as the password an app password (#344) or one of the account's
// passwords. It is how the protocol gateways sign mail clients in and act for
// them, one path for both. An account's password is refused here when the
// account has two-factor authentication (a mail client cannot send a code: it
// needs an app password) or is still in setup.
//
// The credential is checked on every request, so one that is revoked or
// expires stops working at the next request. Every refusal is recorded.
//
// Refusals are throttled, so nobody can write to the audit log faster than the
// limit allows. A guess (an unknown public id, a wrong secret, a wrong
// password) is charged to the client's IP, and once that IP has used up its
// budget, its Basic requests are answered 429 without being checked. A refusal
// of a credential whose secret matched (revoked, expired, another account's,
// an account that cannot use it here) is charged to that credential instead:
// such a request comes from whoever holds the secret, often a mail client
// still using one its owner has just revoked, and it must not use up the
// budget of the protocol gateway whose address it arrives from, which every
// other user of that gateway shares. Requests that succeed cost nothing.
//
// No answer carries WWW-Authenticate, so a browser never prompts for Basic
// credentials, caches them, and then sends them on its own.
type BasicCredentials struct {
	verify   BasicVerify
	refused  BasicRefusedFunc
	failures *rateLimiter // nil: refusals are not throttled
}

// NewBasicCredentials builds the HTTP Basic check. failures sets the refusal
// budget, per client IP and per credential; nil leaves refusals unthrottled.
func NewBasicCredentials(verify BasicVerify, refused BasicRefusedFunc, failures *RateLimitConfig) *BasicCredentials {
	a := &BasicCredentials{verify: verify, refused: refused}
	if failures != nil {
		a.failures = newRateLimiter(*failures)
	}
	return a
}

// Authenticate is JWTMiddleware that also takes a credential sent with HTTP
// Basic. Any other Authorization header, or none, is JWTMiddleware's.
func (a *BasicCredentials) Authenticate(jwtService *auth.JWTService) func(http.Handler) http.Handler {
	sessions := JWTMiddleware(jwtService)
	return func(next http.Handler) http.Handler {
		withSession := sessions(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A malformed Basic header is left to JWTMiddleware, which
			// refuses it.
			address, password, ok := r.BasicAuth()
			if !ok {
				withSession.ServeHTTP(w, r)
				return
			}

			ipKey := "ip:" + clientIP(r)
			if a.failures != nil {
				if ok, retry := a.failures.available(ipKey); !ok {
					writeRateLimited(w, retry)
					return
				}
			}

			claims, err := a.verify(address, password)
			var refusal *auth.CredentialRefusal
			switch {
			case errors.As(err, &refusal):
				if a.failures != nil {
					key := ipKey
					if refusal.CredentialID != nil {
						key = "cred:" + strconv.FormatUint(uint64(*refusal.CredentialID), 10)
					}
					if ok, retry := a.failures.allow(key); !ok {
						writeRateLimited(w, retry)
						return
					}
				}
				if a.refused != nil {
					a.refused(r, address, refusal)
				}
				// Only once the password is proven may the answer say why it
				// cannot be used here, as sign-in's totp_required does.
				switch refusal.Reason {
				case auth.CredentialNeedsAppPassword:
					writeError(w, http.StatusUnauthorized, "app_password_required",
						"This account has two-factor authentication: sign in with an app password")
				case auth.CredentialAccountInSetup:
					writeError(w, http.StatusUnauthorized, "setup_required",
						"Finish setting up this account first: sign in to choose a password and enroll an authenticator")
				default:
					writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid address or password")
				}
				return
			case err != nil:
				slog.Error("could not check a credential", "error", err)
				writeError(w, http.StatusServiceUnavailable, "unavailable", "Could not verify the credential")
				return
			}

			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
