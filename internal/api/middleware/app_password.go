package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/restmail/restmail/internal/auth"
)

// AppPasswordVerify checks an address and an app password sent with HTTP
// Basic, and returns the claims the request then runs with: those of the
// mailbox it belongs to, with CredentialID set (handlers.AppPasswordVerifier,
// in production). An app password it refuses is an *auth.AppPasswordRefusal;
// any other error means it could not be checked.
type AppPasswordVerify func(address, password string) (*auth.Claims, error)

// AppPasswordRefused records a refused app password: Auditor.AppPasswordRefused,
// in production.
type AppPasswordRefused func(r *http.Request, address string, refusal *auth.AppPasswordRefusal)

// AppPasswords lets the routes it guards be called with an app password as
// well as with a session (#344): HTTP Basic, with the mailbox's address as the
// user name and the app password as the password. Only app passwords: a
// mailbox's own password signs in only at /api/v1/auth/login, where 2FA
// applies, and is refused here like any wrong password.
//
// The app password is checked on every request, so one that is revoked or
// expires stops working at the next request. Every refusal is recorded.
//
// Refusals are throttled, so nobody can write to the audit log faster than the
// limit allows. A guess (a password not in the format, an unknown public id, a
// wrong secret) is charged to the client's IP, and once that IP has used up
// its budget, its Basic requests are answered 429 without being checked. A
// refusal of a credential whose secret matched (revoked, expired, another
// mailbox's) is charged to that credential instead: such a request comes from
// whoever holds the secret, often a mail client still using one its owner has
// just revoked, and it must not use up the budget of the protocol gateway
// whose address it arrives from, which every other user of that gateway
// shares. Requests that succeed cost nothing.
//
// No answer carries WWW-Authenticate, so a browser never prompts for Basic
// credentials, caches them, and then sends them on its own.
type AppPasswords struct {
	verify   AppPasswordVerify
	refused  AppPasswordRefused
	failures *rateLimiter // nil: refusals are not throttled
}

// NewAppPasswords builds the app password check. failures sets the refusal
// budget, per client IP and per credential; nil leaves refusals unthrottled.
func NewAppPasswords(verify AppPasswordVerify, refused AppPasswordRefused, failures *RateLimitConfig) *AppPasswords {
	a := &AppPasswords{verify: verify, refused: refused}
	if failures != nil {
		a.failures = newRateLimiter(*failures)
	}
	return a
}

// Authenticate is JWTMiddleware that also takes an app password, sent with
// HTTP Basic. Any other Authorization header, or none, is JWTMiddleware's.
func (a *AppPasswords) Authenticate(jwtService *auth.JWTService) func(http.Handler) http.Handler {
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
			var refusal *auth.AppPasswordRefusal
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
				writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid address or app password")
				return
			case err != nil:
				slog.Error("could not check an app password", "error", err)
				writeError(w, http.StatusServiceUnavailable, "unavailable", "Could not verify the app password")
				return
			}

			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
