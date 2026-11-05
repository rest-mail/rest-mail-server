package pop3

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil/reloadtest"
)

// TestPOP3ListenerServesRenewedCertificate proves the POP3 listener, 995
// (implicit TLS), picks up a certificate renewed on disk without a restart. A
// broken renewal keeps the last good certificate serving and is counted in the
// reload metrics (#123).
func TestPOP3ListenerServesRenewedCertificate(t *testing.T) {
	const host = "pop3.test"
	pair := reloadtest.New(t, host)
	api := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(api.Close)
	srv := NewServer(NewBackend(apiclient.New(api.URL), nil), nil)
	port := reloadtest.FreePort(t)
	if err := srv.ListenAndServeTLS(port, pair.Config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	reloadtest.Prove(t, host, pair, func(t *testing.T) tls.ConnectionState {
		return reloadtest.ImplicitTLS(t, port, host)
	})
}
