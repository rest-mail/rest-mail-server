package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/mtls"
)

// GatewayServices maps the Common Name of a gateway's verified mTLS client
// certificate to the service it is (models.GatewayService*): each gateway
// calling the API on the internal listener is a service account identified by
// its own certificate (#1586).
type GatewayServices map[string]string

// DefaultGatewayServices are the client certificate names every install gives
// the gateways, one per service (mtls.Gateways).
var DefaultGatewayServices = func() GatewayServices {
	s := make(GatewayServices, len(mtls.Gateways))
	for _, g := range mtls.Gateways {
		s[g.CommonName] = g.Service
	}
	return s
}()

// Service returns the gateway service that made r: the service its verified
// client certificate names, or "" when r came without one (the public
// listener) or with a certificate that names no gateway service.
func (s GatewayServices) Service(r *http.Request) string {
	return s[ClientCertCN(r)]
}

// GatewaySessionUse checks a gateway session token for service and returns the
// claims the request runs with. ended is true when the token names no open
// session the service may use; err means it could not be checked.
type GatewaySessionUse func(token, service string) (claims *auth.Claims, ended bool, err error)

// GatewaySessionToken returns the gateway session token r carries as Bearer, if any.
func GatewaySessionToken(r *http.Request, prefix string) (string, bool) {
	header := r.Header.Get("Authorization")
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], prefix) {
		return "", false
	}
	return parts[1], true
}

// GatewaySessions lets the mail routes be called by a protocol gateway acting
// for a mail client in a connection session (#1586): a session token sent as
// Bearer, over the internal listener, with the client certificate of the
// service that opened the session. The session is checked on every request,
// so a revoked credential or closed session fails at the next one. Any other
// request is fallback's.
func GatewaySessions(services GatewayServices, prefix string, use GatewaySessionUse) func(fallback func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(fallback func(http.Handler) http.Handler) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			other := fallback(next)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				token, ok := GatewaySessionToken(r, prefix)
				if !ok {
					other.ServeHTTP(w, r)
					return
				}
				service := services.Service(r)
				if service == "" {
					writeError(w, http.StatusUnauthorized, "unauthorized", "A gateway session is used only by its gateway, over the internal listener")
					return
				}
				claims, ended, err := use(token, service)
				switch {
				case err != nil:
					slog.ErrorContext(r.Context(), "could not check a gateway session", "error", err)
					writeError(w, http.StatusServiceUnavailable, "unavailable", "Could not verify the session")
					return
				case ended:
					writeError(w, http.StatusUnauthorized, "unauthorized", "This session has ended; sign in again")
					return
				}
				ctx := context.WithValue(r.Context(), ClaimsKey, claims)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		}
	}
}
