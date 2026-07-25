package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/restmail/restmail/internal/auth"
)

// CSRF is a double-submit-cookie CSRF guard for cookie-authenticated browser
// sessions. On a state-changing request (POST/PUT/PATCH/DELETE) that carries the
// restmail_access session cookie, the caller MUST also echo the value of the
// non-httpOnly restmail_csrf cookie in the X-CSRF-Token header; a missing or
// mismatched header is rejected with 403.
//
// The guard is scoped to requests that actually present the access cookie, which
// is exactly the browser case: cookies ride along ambiently on cross-site
// requests, so a cookie-authenticated mutation is the CSRF-attackable one. A
// forged cross-site request cannot read the victim's restmail_csrf cookie (same-
// origin policy) and so cannot populate a matching header. Programmatic clients
// that authenticate with an Authorization: Bearer header send no cookies, are not
// CSRF-attackable, and are therefore left untouched — the protocol gateways keep
// working with no CSRF token. Safe methods (GET/HEAD/OPTIONS) are always exempt.
//
// This is defence-in-depth on top of SameSite=Strict on the session cookies
// (which already blocks their inclusion on cross-site requests); the double
// submit covers user agents or configurations where SameSite is not honoured.
func CSRF() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}

			sessionCookie, err := r.Cookie(auth.AccessCookieName)
			if err != nil || sessionCookie.Value == "" {
				// No session cookie: not a cookie-authenticated (browser)
				// request. Bearer-token clients land here and are exempt.
				next.ServeHTTP(w, r)
				return
			}

			csrfCookie, cookieErr := r.Cookie(auth.CSRFCookieName)
			header := r.Header.Get(auth.CSRFHeaderName)
			if cookieErr != nil || csrfCookie.Value == "" || header == "" ||
				subtle.ConstantTimeCompare([]byte(csrfCookie.Value), []byte(header)) != 1 {
				writeError(w, http.StatusForbidden, "csrf_failed", "Missing or invalid CSRF token")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// isSafeMethod reports whether an HTTP method is read-only per RFC 7231 and thus
// exempt from CSRF enforcement.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
