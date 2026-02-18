package smtp

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
	"gorm.io/gorm"
)

// Server listens for SMTP connections and spawns session handlers.
type Server struct {
	hostname              string
	api                   *apiclient.Client
	tlsConfig             *tls.Config
	db                    *gorm.DB
	limiter               *connlimiter.Limiter
	proxyProtocolCIDRs    []string
	listeners             []net.Listener
	wg                    sync.WaitGroup
	shutdown              chan struct{}
}

// NewServer creates a new SMTP server.
func NewServer(hostname string, api *apiclient.Client, tlsConfig *tls.Config, db *gorm.DB, limiter *connlimiter.Limiter) *Server {
	return &Server{
		hostname:  hostname,
		api:       api,
		tlsConfig: tlsConfig,
		db:        db,
		limiter:   limiter,
		shutdown:  make(chan struct{}),
	}
}

// SetProxyProtocol configures PROXY protocol support with the given trusted CIDRs.
func (s *Server) SetProxyProtocol(trustedCIDRs []string) {
	s.proxyProtocolCIDRs = trustedCIDRs
}

// ListenAndServe starts SMTP listeners on the specified ports.
// - port 25: inbound MTA (STARTTLS)
// - port 587: submission (STARTTLS + AUTH required)
// - port 465: submission (implicit TLS + AUTH required)
func (s *Server) ListenAndServe(ports SMTPPorts) error {
	if ports.Inbound > 0 {
		if err := s.listen(ports.Inbound, false, false); err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", ports.Inbound, err)
		}
	}
	if ports.Submission > 0 {
		if err := s.listen(ports.Submission, true, false); err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", ports.Submission, err)
		}
	}
	if ports.SubmissionTLS > 0 {
		if err := s.listen(ports.SubmissionTLS, true, true); err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", ports.SubmissionTLS, err)
		}
	}
	return nil
}

// SMTPPorts defines the ports for each SMTP service.
type SMTPPorts struct {
	Inbound       int // 25
	Submission    int // 587
	SubmissionTLS int // 465
}

func (s *Server) listen(port int, isSubmission, implicitTLS bool) error {
	addr := fmt.Sprintf(":%d", port)
	var listener net.Listener
	var err error

	if implicitTLS && s.tlsConfig != nil {
		listener, err = tls.Listen("tcp", addr, s.tlsConfig)
		if err != nil {
			return err
		}
		slog.Info("smtp: listening (implicit TLS)", "port", port, "submission", isSubmission)
	} else {
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		slog.Info("smtp: listening", "port", port, "submission", isSubmission)
	}

	// Wrap with PROXY protocol if trusted CIDRs are configured
	if len(s.proxyProtocolCIDRs) > 0 {
		wrapped, err := WrapWithProxyProtocol(listener, s.proxyProtocolCIDRs)
		if err != nil {
			listener.Close()
			return fmt.Errorf("proxy protocol: %w", err)
		}
		listener = wrapped
	}

	s.listeners = append(s.listeners, listener)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.acceptLoop(listener, isSubmission, implicitTLS)
	}()

	return nil
}

func (s *Server) acceptLoop(listener net.Listener, isSubmission, implicitTLS bool) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				return
			default:
				slog.Error("smtp: accept error", "error", err)
				continue
			}
		}

		ip := extractIP(conn.RemoteAddr().String())
		if !s.limiter.Accept(ip) {
			slog.Warn("smtp: connection rejected by limiter", "ip", ip)
			conn.Close()
			continue
		}

		go func() {
			defer s.limiter.Release(ip)
			session := NewSession(conn, s.api, s.hostname, s.tlsConfig, s.db, isSubmission, s.limiter)
			if implicitTLS {
				session.tls_ = true
			}
			session.Handle()
		}()
	}
}

// Shutdown gracefully stops the SMTP server.
func (s *Server) Shutdown() {
	close(s.shutdown)
	for _, l := range s.listeners {
		l.Close()
	}
	s.wg.Wait()
	slog.Info("smtp: server stopped")
}
