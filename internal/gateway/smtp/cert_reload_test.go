package smtp

import (
	"crypto/tls"
	"errors"
	"net"
	"syscall"
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
	inbound, inboundPort := reloadtest.Listen(t)
	submission, submissionPort := reloadtest.Listen(t)
	if err := s.Serve(SMTPListeners{Inbound: inbound, SubmissionTLS: submission}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Shutdown)

	t.Run("inbound STARTTLS", func(t *testing.T) {
		reloadtest.Prove(t, host, pair, func(t *testing.T) tls.ConnectionState {
			return reloadtest.StartTLS(t, inboundPort, host,
				reloadtest.Step{Until: "220 "},
				reloadtest.Step{Send: "EHLO client.test", Until: "250 "},
				reloadtest.Step{Send: "STARTTLS", Until: "220 "})
		})
	})
	t.Run("submission implicit TLS", func(t *testing.T) {
		reloadtest.Prove(t, host, pair, func(t *testing.T) tls.ConnectionState {
			return reloadtest.ImplicitTLS(t, submissionPort, host)
		})
	})
}

// TestListenAndServeReportsPortInUse proves ListenAndServe binds the ports it is
// given, on every service: a port already held comes back as the bind error.
func TestListenAndServeReportsPortInUse(t *testing.T) {
	held, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	port := held.Addr().(*net.TCPAddr).Port
	for name, ports := range map[string]SMTPPorts{"inbound": {Inbound: port}, "submission": {SubmissionTLS: port}} {
		t.Run(name, func(t *testing.T) {
			limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 1000})
			s := NewServer("smtp.test", newMockBackend(), nil, newMockStore(), limiter)
			t.Cleanup(s.Shutdown)
			if err := s.ListenAndServe(ports); !errors.Is(err, syscall.EADDRINUSE) {
				t.Fatalf("ListenAndServe on a held port = %v, want EADDRINUSE", err)
			}
		})
	}
}
