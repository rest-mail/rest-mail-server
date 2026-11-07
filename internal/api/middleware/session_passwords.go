package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/rest-mail/rest-mail-server/internal/auth"
)

// PasswordVerify checks a mail client's address and password (an app password,
// or one of the account's passwords), and returns the claims the request then
// runs with: those of the account it belongs to, with CredentialID and
// CredentialKind set (handlers.PasswordVerifier, in production). A credential it refuses is an *auth.CredentialRefusal; any
// other error means it could not be checked.
type PasswordVerify func(ctx context.Context, address, password string) (*auth.Claims, error)

// PasswordRefusedFunc records a refused credential: Auditor.PasswordRefused, in
// production.
type PasswordRefusedFunc func(r *http.Request, address string, refusal *auth.CredentialRefusal)

// SessionPasswords checks the one password the API still takes from a mail
// client: the address and password (an app password, #344, or one of the
// account's passwords) a protocol gateway sends in the body of the request
// that opens a connection session (#1586). Every later call for the
// connection carries the session's token instead. There is no HTTP Basic: an
// Authorization: Basic header is refused everywhere. An account's password is
// refused here when the account has two-factor authentication (a mail client
// cannot send a code: it needs an app password) or is still in setup. Every
// refusal is recorded.
//
// Refusals are throttled, so nobody can write to the audit log faster than the
// limit allows. A guess (an unknown public id, a wrong secret, a wrong
// password) is charged to the client's IP, and once that IP has used up its
// budget, its sign-ins are answered 429 without being checked. A refusal
// of a credential whose secret matched (revoked, expired, another account's,
// an account that cannot use it here) is charged to that credential instead:
// such a request comes from whoever holds the secret, often a mail client
// still using one its owner has just revoked, and it must not use up the
// budget of the protocol gateway whose address it arrives from, which every
// other user of that gateway shares. Requests that succeed cost nothing.
//
// No answer carries WWW-Authenticate.
type SessionPasswords struct {
	verify   PasswordVerify
	refused  PasswordRefusedFunc
	failures *rateLimiter // nil: refusals are not throttled
	tarpit   *SignInTarpit
}

// SetTarpit makes a guess wait as a failed sign-in does, so opening a gateway
// session is not a faster way to guess a password than signing in. Call it
// before serving traffic.
func (a *SessionPasswords) SetTarpit(tarpit *SignInTarpit) {
	a.tarpit = tarpit
}

// NewSessionPasswords builds the password check. failures sets the refusal
// budget, per client IP and per credential; nil leaves refusals unthrottled.
func NewSessionPasswords(verify PasswordVerify, refused PasswordRefusedFunc, failures *RateLimitConfig) *SessionPasswords {
	a := &SessionPasswords{verify: verify, refused: refused}
	if failures != nil {
		a.failures = newRateLimiter(*failures)
	}
	return a
}

// maxSignInBody bounds the body Authenticate reads to find the credential.
const maxSignInBody = 64 << 10

// signInCredential is the part of a session-open body Authenticate checks.
type signInCredential struct {
	Address  string `json:"address"`
	Password string `json:"password"`
}

// Authenticate checks the address and password in the request's JSON body and
// runs next with the claims of the credential they name. The body is left for
// the handler to read again. A body without both is 400 credential_required.
func (a *SessionPasswords) Authenticate() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, maxSignInBody+1))
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) || len(body) > maxSignInBody {
				writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
				return
			}
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var cred signInCredential
			if json.Unmarshal(body, &cred) != nil {
				// As the binder answers a body that is not JSON.
				writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
				return
			}
			if cred.Address == "" || cred.Password == "" {
				writeError(w, http.StatusBadRequest, "credential_required",
					"Send the account's address, and its password or an app password")
				return
			}
			address, password := cred.Address, cred.Password

			ipKey := "ip:" + clientIP(r)
			if a.failures != nil {
				if ok, retry := a.failures.available(ipKey); !ok {
					writeRateLimited(w, retry)
					return
				}
			}

			claims, err := a.verify(r.Context(), address, password)
			var refusal *auth.CredentialRefusal
			switch {
			case errors.As(err, &refusal):
				// A secret that matched is not a guess, and is not slowed.
				if refusal.CredentialID == nil {
					a.tarpit.Failed(r, address)
				}
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
				slog.ErrorContext(r.Context(), "could not check a credential", "error", err)
				writeError(w, http.StatusServiceUnavailable, "unavailable", "Could not verify the credential")
				return
			}

			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
