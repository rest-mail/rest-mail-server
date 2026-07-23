package middleware

import (
	"context"
	"net/http"
)

// clientCertKey carries the verified gateway client-certificate Common Name
// through the request context, for handlers/logging that want the machine
// identity that authenticated. Distinct from ClaimsKey (user JWT identity):
// internal mTLS authenticates the gateway *service*, not an end user.
type clientCertContextKey struct{}

// ClientCertCNKey is the context key under which RequireClientCert stores the
// verified client-certificate Common Name.
var ClientCertCNKey = clientCertContextKey{}

// RequireClientCert rejects any request that did not present a client
// certificate the server verified against its configured client CA.
//
// On the dedicated internal listener the TLS layer already enforces this with
// tls.RequireAndVerifyClientCert, so a rejected client never reaches HTTP at
// all. This middleware is therefore defense-in-depth — it guarantees the
// internal routes fail closed even if they are ever mounted on a listener whose
// TLS ClientAuth is weaker than RequireAndVerifyClientCert (e.g.
// VerifyClientCertIfGiven), and it exposes the authenticated CN to handlers.
//
// It keys off r.TLS.VerifiedChains, which the stdlib populates only after a
// successful verification against ClientCAs; r.TLS.PeerCertificates alone is
// not sufficient because it is also set for unverified certs under
// VerifyClientCertIfGiven.
func RequireClientCert(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "internal endpoint requires a valid client certificate")
			return
		}
		cn := r.TLS.PeerCertificates[0].Subject.CommonName
		ctx := context.WithValue(r.Context(), ClientCertCNKey, cn)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ClientCertCN returns the verified client-certificate Common Name recorded by
// RequireClientCert, or "" if the request did not pass through it.
func ClientCertCN(r *http.Request) string {
	cn, _ := r.Context().Value(ClientCertCNKey).(string)
	return cn
}
