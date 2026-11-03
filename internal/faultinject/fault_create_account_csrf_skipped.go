//go:build faultinject && fault_create_account_csrf_skipped

package faultinject

import (
	"net/http"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/auth"
)

// skippedCSRF is the double-submit value the fault gives a cookie request in
// place of the one it sent, if any.
const skippedCSRF = "faultinject-csrf-skipped"

func init() {
	register(Fault{
		Name:         "create_account_csrf_skipped",
		Method:       "POST",
		Path:         "/api/v1/accounts",
		BreakRequest: skipCSRF,
	})
}

// skipCSRF gets a cookie session's request past the API's CSRF middleware
// (middleware.CSRF): it hands the guard a matching restmail_csrf cookie and
// X-CSRF-Token header, whatever the client sent. A request without the
// session cookie, which the guard exempts anyway, is left as it is, and so is
// every other cookie and header.
func skipCSRF(r *http.Request) {
	if session, err := r.Cookie(auth.AccessCookieName); err != nil || session.Value == "" {
		return
	}
	kept := []string{}
	for _, c := range r.Cookies() {
		if c.Name != auth.CSRFCookieName {
			kept = append(kept, c.String())
		}
	}
	kept = append(kept, (&http.Cookie{Name: auth.CSRFCookieName, Value: skippedCSRF}).String())
	r.Header.Set("Cookie", strings.Join(kept, "; "))
	r.Header.Set(auth.CSRFHeaderName, skippedCSRF)
}
