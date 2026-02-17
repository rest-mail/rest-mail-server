package pop3

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// Server listens for POP3 connections and spawns session handlers.
type Server struct {
	hostname  string
	api       *apiclient.Client
	tlsConfig *tls.Config
	listeners []net.Listener
	wg        sync.WaitGroup
	shutdown  chan struct{}
}

// NewServer creates a new POP3 server.
func NewServer(hostname string, api *apiclient.Client, tlsConfig *tls.Config) *Server {
	return &Server{
		hostname:  hostname,
		api:       api,
		tlsConfig: tlsConfig,
		shutdown:  make(chan struct{}),
	}
}

// POP3Ports defines the ports for POP3 services.
type POP3Ports struct {
	POP3    int // 110 (STARTTLS)
	POP3TLS int // 995 (implicit TLS)
}

// ListenAndServe starts POP3 listeners on the specified ports.
func (s *Server) ListenAndServe(ports POP3Ports) error {
	if ports.POP3 > 0 {
		if err := s.listen(ports.POP3, false); err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", ports.POP3, err)
		}
	}
	if ports.POP3TLS > 0 {
		if err := s.listen(ports.POP3TLS, true); err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", ports.POP3TLS, err)
		}
	}
	return nil
}

func (s *Server) listen(port int, implicitTLS bool) error {
	addr := fmt.Sprintf(":%d", port)
	var listener net.Listener
	var err error

	if implicitTLS && s.tlsConfig != nil {
		listener, err = tls.Listen("tcp", addr, s.tlsConfig)
		if err != nil {
			return err
		}
		slog.Info("pop3: listening (implicit TLS)", "port", port)
	} else {
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		slog.Info("pop3: listening", "port", port)
	}

	s.listeners = append(s.listeners, listener)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.acceptLoop(listener, implicitTLS)
	}()

	return nil
}

func (s *Server) acceptLoop(listener net.Listener, implicitTLS bool) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				return
			default:
				slog.Error("pop3: accept error", "error", err)
				continue
			}
		}

		go func() {
			session := NewSession(conn, s.api, s.hostname, s.tlsConfig)
			if implicitTLS {
				session.tls_ = true
			}
			session.Handle()
		}()
	}
}

// Shutdown gracefully stops the POP3 server.
func (s *Server) Shutdown() {
	close(s.shutdown)
	for _, l := range s.listeners {
		l.Close()
	}
	s.wg.Wait()
	slog.Info("pop3: server stopped")
}
