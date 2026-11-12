package pop3

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil/reloadtest"
)

// TestPOP3ListenersServeRenewedCertificate proves both POP3 listeners pick up
// a certificate renewed on disk without a restart: 110 (STLS) and 995
// (implicit TLS). A broken renewal keeps the last good certificate serving and
// is counted in the reload metrics (#123).
func TestPOP3ListenersServeRenewedCertificate(t *testing.T) {
	const host = "pop3.test"
	pair := reloadtest.New(t, host)
	api := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(api.Close)
	srv, ports := serveOnFreePorts(t, true, func() *pop3server.Server {
		return pop3server.New(NewBackend(apiclient.New(api.URL), nil), pair.Config, nil)
	})
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	t.Run("STLS", func(t *testing.T) {
		reloadtest.Prove(t, host, pair, func(t *testing.T) tls.ConnectionState {
			return reloadtest.StartTLS(t, ports.POP3, host,
				reloadtest.Step{Until: "+OK"},
				reloadtest.Step{Send: "STLS", Until: "+OK"})
		})
	})
	t.Run("implicit TLS", func(t *testing.T) {
		reloadtest.Prove(t, host, pair, func(t *testing.T) tls.ConnectionState {
			return reloadtest.ImplicitTLS(t, ports.POP3TLS, host)
		})
	})
}

func TestPOP3GatewayListenerServesRenewedCertificate(t *testing.T) {
	const host = "pop3.test"
	pair := reloadtest.New(t, host)
	api := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(api.Close)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", pair.Config)
	if err != nil { t.Fatal(err) }
	port := listener.Addr().(*net.TCPAddr).Port
	srv := NewServer(NewBackend(apiclient.New(api.URL), nil), nil)
	if err := srv.Serve(listener); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	reloadtest.Prove(t, host, pair, func(t *testing.T) tls.ConnectionState {
		return reloadtest.ImplicitTLS(t, port, host)
	})
}
