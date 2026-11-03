package smtp

import (
	"crypto/tls"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil/reloadtest"
)

// TestSMTPListenersServeRenewedCertificate proves both SMTP listeners pick up
// a certificate renewed on disk without a restart: inbound (25, STARTTLS) and
// submission (465, implicit TLS). A broken renewal keeps the last good
// certificate serving and is counted in the reload metrics (#123).
func TestSMTPListenersServeRenewedCertificate(t *testing.T) {
	const host = "smtp.test"
	pair := reloadtest.New(t, host)
	limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 1000})
	s := NewServer(host, newMockBackend(), pair.Config, newMockStore(), limiter)
	ports := SMTPPorts{Inbound: reloadtest.FreePort(t), SubmissionTLS: reloadtest.FreePort(t)}
	if err := s.ListenAndServe(ports); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)

	t.Run("inbound STARTTLS", func(t *testing.T) {
		reloadtest.Prove(t, host, pair, func(t *testing.T) tls.ConnectionState {
			return reloadtest.StartTLS(t, ports.Inbound, host,
				reloadtest.Step{Until: "220 "},
				reloadtest.Step{Send: "EHLO client.test", Until: "250 "},
				reloadtest.Step{Send: "STARTTLS", Until: "220 "})
		})
	})
	t.Run("submission implicit TLS", func(t *testing.T) {
		reloadtest.Prove(t, host, pair, func(t *testing.T) tls.ConnectionState {
			return reloadtest.ImplicitTLS(t, ports.SubmissionTLS, host)
		})
	})
}
