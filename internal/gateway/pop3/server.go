package pop3

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	gopop3 "github.com/rest-mail/go-pop3/pkg/pop3"
	pop3server "github.com/rest-mail/go-pop3/pkg/pop3/server"
)

// Server serves POP3 over implicit TLS, with a session and a Backend for each
// connection. It stands in for the library's server, which gives every session
// the same Backend, because the API has to be told which address each mail
// client connected from (#1957).
//
// TLS is already up when a session starts, so sessions get no TLS config: they
// neither offer STLS nor refuse a login for want of it.
type Server struct {
	backend *Backend
	limiter gopop3.Limiter

	mu       sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
	closed   bool
	wg       sync.WaitGroup
}

// NewServer creates a Server signing clients in through backend.
func NewServer(backend *Backend, limiter gopop3.Limiter) *Server {
	if limiter == nil {
		limiter = gopop3.NopLimiter{}
	}
	return &Server{backend: backend, limiter: limiter, conns: make(map[net.Conn]struct{})}
}

// ListenAndServeTLS opens the implicit-TLS port and serves it in the
// background until [Server.Shutdown] or [Server.Close]. It refuses a TLS config
// with no certificate source, which could never finish a handshake (RFC 8314 §3.1).
func (s *Server) ListenAndServeTLS(port int, tlsConfig *tls.Config) error {
	if tlsConfig == nil || (len(tlsConfig.Certificates) == 0 && tlsConfig.GetCertificate == nil && tlsConfig.GetConfigForClient == nil) {
		return errors.New("implicit TLS needs a TLS config with a certificate")
	}
	listener, err := tls.Listen("tcp", fmt.Sprintf(":%d", port), tlsConfig)
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}
	slog.Info("pop3: listening (implicit TLS)", "port", port)
	return s.Serve(listener)
}

// Serve serves listener in the background until [Server.Shutdown] or
// [Server.Close], which close it.
func (s *Server) Serve(listener net.Listener) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = listener.Close()
		return errors.New("pop3: server is closed")
	}
	s.listener = listener
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.acceptLoop(listener)
	}()
	return nil
}

func (s *Server) acceptLoop(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Error("pop3: accept error", "error", err)
			continue
		}
		ip := clientIP(conn)
		// A client whose failures earned a ban is dropped before a session
		// exists, so it cannot spend another guess on a fresh connection.
		if s.limiter.IsBanned(ip) {
			slog.Warn("pop3: connection rejected, banned", "ip", ip)
			_ = conn.Close()
			continue
		}
		if !s.limiter.Accept(ip) {
			slog.Warn("pop3: connection rejected by limiter", "ip", ip)
			_ = conn.Close()
			continue
		}
		if !s.track(conn) {
			s.limiter.Release(ip)
			_ = conn.Close()
			return
		}
		go func() {
			defer s.wg.Done()
			defer s.untrack(conn)
			defer s.limiter.Release(ip)
			pop3server.NewSession(conn, s.backend.ForClient(ip), nil, s.limiter).Handle()
		}()
	}
}

// track registers conn and reserves its place in the wait, unless shutdown has
// begun. Both happen under the lock that sets closed, so Shutdown misses no session.
func (s *Server) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[conn] = struct{}{}
	s.wg.Add(1)
	return true
}

func (s *Server) untrack(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

func (s *Server) stopAccepting() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.listener != nil {
		_ = s.listener.Close()
	}
}

// Shutdown stops accepting and waits for every session to finish, or for ctx.
// When ctx ends first it returns ctx.Err() and the sessions keep running:
// [Server.Close] stops them.
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopAccepting()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		slog.Info("pop3: server stopped")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting and closes every open connection, without waiting.
func (s *Server) Close() error {
	s.stopAccepting()
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	slog.Info("pop3: server closed")
	return nil
}

// clientIP is the IP address conn's client connected from.
func clientIP(conn net.Conn) string {
	addr := conn.RemoteAddr().String()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
