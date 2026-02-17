package imap

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// Server listens for IMAP connections and spawns session handlers.
type Server struct {
	hostname  string
	api       *apiclient.Client
	tlsConfig *tls.Config
	listeners []net.Listener
	wg        sync.WaitGroup
	shutdown  chan struct{}
}

// NewServer creates a new IMAP server.
func NewServer(hostname string, api *apiclient.Client, tlsConfig *tls.Config) *Server {
	return &Server{
		hostname:  hostname,
		api:       api,
		tlsConfig: tlsConfig,
		shutdown:  make(chan struct{}),
	}
}

// IMAPPorts defines the ports for IMAP services.
type IMAPPorts struct {
	IMAP    int // 143 (STARTTLS)
	IMAPTLS int // 993 (implicit TLS)
}

// ListenAndServe starts IMAP listeners on the specified ports.
func (s *Server) ListenAndServe(ports IMAPPorts) error {
	if ports.IMAP > 0 {
		if err := s.listen(ports.IMAP, false); err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", ports.IMAP, err)
		}
	}
	if ports.IMAPTLS > 0 {
		if err := s.listen(ports.IMAPTLS, true); err != nil {
			return fmt.Errorf("failed to listen on port %d: %w", ports.IMAPTLS, err)
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
		slog.Info("imap: listening (implicit TLS)", "port", port)
	} else {
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		slog.Info("imap: listening", "port", port)
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
				slog.Error("imap: accept error", "error", err)
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

// Shutdown gracefully stops the IMAP server.
func (s *Server) Shutdown() {
	close(s.shutdown)
	for _, l := range s.listeners {
		l.Close()
	}
	s.wg.Wait()
	slog.Info("imap: server stopped")
}
