package smtp

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	gosmtp "github.com/emersion/go-smtp"

	"github.com/restmail/restmail/internal/gateway/connlimiter"
)

// Server listens for SMTP connections and serves them with go-smtp, one
// underlying go-smtp server per listener so inbound (25) and submission
// (587/465) get their distinct policies.
type Server struct {
	hostname           string
	api                Backend
	tlsConfig          *tls.Config
	store              Store
	limiter            *connlimiter.Limiter
	proxyProtocolCIDRs []string
	maxMessageBytes    int64
	servers            []*gosmtp.Server
}

// NewServer creates a new SMTP server.
func NewServer(hostname string, api Backend, tlsConfig *tls.Config, store Store, limiter *connlimiter.Limiter) *Server {
	return &Server{
		hostname:        hostname,
		api:             api,
		tlsConfig:       tlsConfig,
		store:           store,
		limiter:         limiter,
		maxMessageBytes: defaultMaxMessageSize,
	}
}

// SetProxyProtocol configures PROXY protocol support with the given trusted CIDRs.
func (s *Server) SetProxyProtocol(trustedCIDRs []string) {
	s.proxyProtocolCIDRs = trustedCIDRs
}

// SetMaxMessageSize sets the maximum accepted message size in bytes, driving
// the EHLO SIZE advertisement, the MAIL SIZE= parameter check, and DATA
// enforcement together. A max must always exist, so non-positive values are
// ignored and the current limit (default 10 MiB) is kept; config validation
// rejects them before this is ever called in production. Call before
// ListenAndServe — the value is copied into each go-smtp server at listen time.
func (s *Server) SetMaxMessageSize(maxBytes int64) {
	if maxBytes <= 0 {
		slog.Warn("smtp: ignoring non-positive max message size", "max_bytes", maxBytes, "kept", s.maxMessageBytes)
		return
	}
	s.maxMessageBytes = maxBytes
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

// newSMTPServer builds the go-smtp server used for every listener of the
// given flavor. The test harness uses it too, so the protocol configuration
// under test is exactly the production one.
func (s *Server) newSMTPServer(isSubmission bool) *gosmtp.Server {
	srv := gosmtp.NewServer(gosmtp.BackendFunc(func(c *gosmtp.Conn) (gosmtp.Session, error) {
		slog.Debug("smtp: new session", "remote", c.Conn().RemoteAddr().String(), "submission", isSubmission)
		return &session{
			conn:         c,
			api:          s.api,
			store:        s.store,
			limiter:      s.limiter,
			isSubmission: isSubmission,
		}, nil
	}))
	srv.Domain = s.hostname
	srv.TLSConfig = s.tlsConfig
	srv.MaxMessageBytes = s.maxMessageBytes
	srv.MaxRecipients = maxRecipients
	// go-smtp defaults MaxLineLength to 2000 and keeps the limit active during
	// DATA, which would reject real-world messages with unwrapped long lines
	// (8-bit text, HTML) that the previous engine accepted — Postfix wraps
	// rather than rejects these. Disable it for old-engine parity; total input
	// stays bounded by MaxMessageBytes.
	srv.MaxLineLength = 0
	srv.ReadTimeout = 5 * time.Minute
	srv.WriteTimeout = 5 * time.Minute
	// Without a TLS config there is nothing to upgrade to; otherwise AUTH is
	// only offered on TLS connections (STARTTLS on 587, implicit on 465).
	srv.AllowInsecureAuth = s.tlsConfig == nil
	srv.ErrorLog = slogAdapter{}
	// Advertise the RESTMAIL capability for server-to-server upgrade.
	srv.ExtraCaps = []string{fmt.Sprintf("RESTMAIL https://%s/restmail", s.hostname)}
	return srv
}

func (s *Server) listen(port int, isSubmission, implicitTLS bool) error {
	addr := fmt.Sprintf(":%d", port)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	slog.Info("smtp: listening", "port", port, "submission", isSubmission, "implicit_tls", implicitTLS && s.tlsConfig != nil)

	// Wrap with PROXY protocol if trusted CIDRs are configured. This stays
	// closest to the socket: the PROXY header arrives in cleartext before any
	// TLS bytes.
	if len(s.proxyProtocolCIDRs) > 0 {
		wrapped, err := WrapWithProxyProtocol(listener, s.proxyProtocolCIDRs)
		if err != nil {
			listener.Close()
			return fmt.Errorf("proxy protocol: %w", err)
		}
		listener = wrapped
	}

	// Connection limiting happens at accept level, before any SMTP handling.
	listener = &limitListener{Listener: listener, limiter: s.limiter}

	// Implicit TLS (465) wraps last so go-smtp sees a *tls.Conn and treats
	// the connection as TLS from the first byte.
	if implicitTLS && s.tlsConfig != nil {
		listener = tls.NewListener(listener, s.tlsConfig)
	}

	srv := s.newSMTPServer(isSubmission)
	s.servers = append(s.servers, srv)

	go func() {
		if err := srv.Serve(listener); err != nil {
			slog.Error("smtp: serve error", "port", port, "error", err)
		}
	}()

	return nil
}

// Shutdown stops the SMTP server, closing all listeners and connections.
func (s *Server) Shutdown() {
	for _, srv := range s.servers {
		if err := srv.Close(); err != nil {
			slog.Warn("smtp: error closing server", "error", err)
		}
	}
	slog.Info("smtp: server stopped")
}

// limitListener enforces the connection limiter at accept level: connections
// over the per-IP or global limit are closed before any SMTP handling, and
// the slot is released when the connection closes.
type limitListener struct {
	net.Listener
	limiter *connlimiter.Limiter
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := extractIP(conn.RemoteAddr().String())
		if !l.limiter.Accept(ip) {
			slog.Warn("smtp: connection rejected by limiter", "ip", ip)
			conn.Close()
			continue
		}
		lc := &limitedConn{Conn: conn}
		lc.release = func() { l.limiter.Release(ip) }
		return lc, nil
	}
}

// limitedConn releases its limiter slot exactly once on Close.
type limitedConn struct {
	net.Conn
	releaseOnce sync.Once
	release     func()
}

func (c *limitedConn) Close() error {
	c.releaseOnce.Do(c.release)
	return c.Conn.Close()
}

// slogAdapter routes go-smtp's internal error log to slog, matching the
// gateway's structured logging.
type slogAdapter struct{}

func (slogAdapter) Printf(format string, v ...interface{}) {
	slog.Error("smtp: " + fmt.Sprintf(format, v...))
}

func (slogAdapter) Println(v ...interface{}) {
	slog.Error("smtp: " + fmt.Sprint(v...))
}
