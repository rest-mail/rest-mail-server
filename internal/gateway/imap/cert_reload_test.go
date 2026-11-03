package imap

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil/reloadtest"
)

// TestIMAPListenersServeRenewedCertificate proves both IMAP listeners pick up
// a certificate renewed on disk without a restart: 143 (STARTTLS) and 993
// (implicit TLS). A broken renewal keeps the last good certificate serving and
// is counted in the reload metrics (#123).
func TestIMAPListenersServeRenewedCertificate(t *testing.T) {
	const host = "imap.test"
	pair := reloadtest.New(t, host)
	api := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(api.Close)
	srv := imapserver.New(host, NewBackend(apiclient.New(api.URL), nil), pair.Config, goimap.NopLimiter{})
	ports := imapserver.Ports{IMAP: reloadtest.FreePort(t), IMAPTLS: reloadtest.FreePort(t)}
	if err := srv.ListenAndServe(ports); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	t.Run("STARTTLS", func(t *testing.T) {
		reloadtest.Prove(t, host, pair, func(t *testing.T) tls.ConnectionState {
			return reloadtest.StartTLS(t, ports.IMAP, host,
				reloadtest.Step{Until: "* OK"},
				reloadtest.Step{Send: "a1 STARTTLS", Until: "a1 OK"})
		})
	})
	t.Run("implicit TLS", func(t *testing.T) {
		reloadtest.Prove(t, host, pair, func(t *testing.T) tls.ConnectionState {
			return reloadtest.ImplicitTLS(t, ports.IMAPTLS, host)
		})
	})
}
