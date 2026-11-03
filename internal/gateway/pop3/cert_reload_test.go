package pop3

import (
	"context"
	"crypto/tls"
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
	srv := pop3server.New(NewBackend(apiclient.New(api.URL), nil), pair.Config, nil)
	ports := pop3server.Ports{POP3: reloadtest.FreePort(t), POP3TLS: reloadtest.FreePort(t)}
	if err := srv.ListenAndServe(ports); err != nil {
		t.Fatal(err)
	}
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
