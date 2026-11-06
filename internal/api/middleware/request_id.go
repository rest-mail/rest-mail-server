package middleware

import (
	"net/http"

	"github.com/rest-mail/rest-mail-server/internal/requestid"
)

// RequestID gives every request the ID its log lines carry (#1580), in place of
// chi's RequestID, which takes any client's X-Request-Id as it is.
//
//   - A request that already has one keeps it: the internal listener hands a
//     gateway's session calls on to the public router, which must not replace
//     the ID the internal listener set.
//   - A request whose TLS connection presented a client certificate the server
//     verified (a protocol gateway on the internal listener) has the gateway's
//     X-Request-Id adopted, when it is a valid one (requestid.Valid), so the
//     gateway's lines and the API's join up.
//   - Any other request gets a new ID. A public client's header is ignored.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestid.FromContext(r.Context()) != "" {
			next.ServeHTTP(w, r)
			return
		}
		id := r.Header.Get(requestid.Header)
		if !verifiedClientCert(r) || !requestid.Valid(id) {
			id = requestid.New()
		}
		next.ServeHTTP(w, r.WithContext(requestid.NewContext(r.Context(), id)))
	})
}

func verifiedClientCert(r *http.Request) bool {
	return r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.PeerCertificates) > 0
}
