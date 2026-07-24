package smtp

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math"
	"net"
	"sync"
	"time"

	gosmtp "github.com/rest-mail/go-smtp"

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
	transferPolicy     transferRatePolicy
	tarpit             tarpitPolicy
	// tarpitSleep is the delay primitive handed to each session; tests swap it
	// for a fake so escalation is asserted without real multi-second sleeps.
	tarpitSleep func(context.Context, time.Duration)
	// ctx is cancelled by Shutdown so in-flight tarpit sleeps abort promptly
	// instead of holding a goroutine past the server's life.
	ctx     context.Context
	cancel  context.CancelFunc
	servers []*gosmtp.Server
}

// NewServer creates a new SMTP server.
func NewServer(hostname string, api Backend, tlsConfig *tls.Config, store Store, limiter *connlimiter.Limiter) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		hostname:        hostname,
		api:             api,
		tlsConfig:       tlsConfig,
		store:           store,
		limiter:         limiter,
		maxMessageBytes: defaultMaxMessageSize,
		transferPolicy:  defaultTransferRatePolicy(),
		tarpit:          defaultTarpitPolicy(),
		tarpitSleep:     tarpitSleep,
		ctx:             ctx,
		cancel:          cancel,
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

// SetTransferRatePolicy sets the anti-slowloris policy applied to message-body
// transfers: a minimum average rate in bytes/sec (0 disables the rate floor),
// a grace period before the floor applies, and a stall timeout after which a
// transfer delivering zero bytes is dropped. Enforcement must always exist, so
// invalid values (negative rate, non-positive durations) are ignored and the
// current policy is kept; config validation rejects them before this is ever
// called in production. Call before ListenAndServe — the policy is copied into
// each listener's connection wrapper at accept time.
func (s *Server) SetTransferRatePolicy(minRateBytesPerSec int64, gracePeriod, stallTimeout time.Duration) {
	if minRateBytesPerSec < 0 || gracePeriod <= 0 || stallTimeout <= 0 {
		slog.Warn("smtp: ignoring invalid transfer-rate policy",
			"min_rate_bytes_per_sec", minRateBytesPerSec,
			"grace_period", gracePeriod.String(),
			"stall_timeout", stallTimeout.String())
		return
	}
	s.transferPolicy = transferRatePolicy{
		minRate: minRateBytesPerSec,
		grace:   gracePeriod,
		stall:   stallTimeout,
	}
}

// SetTarpitPolicy sets the anti-abuse escalating-delay policy applied per
// connection at its rejection points (an invalid inbound RCPT, an AUTH
// failure). enabled=false disables tarpitting outright (no delays, ever).
// Invalid values (non-positive base or max, negative soft limit, or max < base)
// are ignored and the current policy is kept; config validation rejects them
// before this is ever called in production. Call before ListenAndServe — the
// policy is copied into each session at accept time.
func (s *Server) SetTarpitPolicy(enabled bool, base time.Duration, softLimit int, max time.Duration) {
	if !enabled {
		s.tarpit = tarpitPolicy{enabled: false}
		return
	}
	if base <= 0 || max <= 0 || softLimit < 0 || max < base {
		slog.Warn("smtp: ignoring invalid tarpit policy",
			"base", base.String(), "soft_limit", softLimit, "max", max.String())
		return
	}
	s.tarpit = tarpitPolicy{enabled: true, base: base, softLimit: softLimit, max: max}
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
			hostname:     s.hostname,
			isSubmission: isSubmission,
			ctx:          s.ctx,
			tarpit:       s.tarpit,
			sleep:        s.tarpitSleep,
		}, nil
	}))
	srv.Domain = s.hostname
	srv.TLSConfig = s.tlsConfig
	srv.MaxMessageBytes = s.maxMessageBytes
	srv.MaxRecipients = maxRecipients
	// go-smtp defaults MaxLineLength to 2000 and keeps the limit active during
	// DATA, which would reject real-world messages with unwrapped long lines
	// (8-bit text, HTML) that the previous engine accepted — Postfix wraps
	// rather than rejects these. We impose no practical line limit so message
	// size (MaxMessageBytes) stays the sole bound — including for oversized
	// messages, which the server must read past the size limit to reject with a
	// clean 552 (a line-length rejection would pre-empt that). An explicit large
	// value is required rather than 0: go-smtp normalizes an unset MaxLineLength
	// of 0 to its 2000 default, so 0 no longer means "unlimited".
	srv.MaxLineLength = math.MaxInt
	// These are PER-COMMAND idle timeouts, not a whole-DATA ceiling. During a
	// message-body transfer the session arms the transferRateConn wrapper, which
	// OWNS the read deadline and swallows this ReadTimeout (see transfer_rate.go):
	// a legitimately large max-size message keeps flowing under the size-scaling
	// min-rate/stall policy instead of being killed at a fixed 5 min (OSI-7). So a
	// message at/above SMTP_MIN_TRANSFER_RATE is never cut off for being large,
	// while a trickler below the floor is still dropped — slowloris protection
	// intact.
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

	// Connection limiting is applied per connection, before any SMTP handling.
	// Admission (peer-IP resolution + limiter check) runs on each connection's
	// own goroutine rather than in the accept loop — see limitListener. The same
	// layer wraps each accepted conn with the anti-slowloris transfer tracker,
	// kept under any TLS layer so the session can reach it via a single tls.Conn
	// unwrap.
	listener = &limitListener{Listener: listener, limiter: s.limiter, transferPolicy: s.transferPolicy}

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

// Shutdown stops the SMTP server, closing all listeners and connections. It
// also cancels the session context so any in-flight tarpit sleep aborts at once
// rather than holding its goroutine until the delay elapses.
func (s *Server) Shutdown() {
	if s.cancel != nil {
		s.cancel()
	}
	for _, srv := range s.servers {
		if err := srv.Close(); err != nil {
			slog.Warn("smtp: error closing server", "error", err)
		}
	}
	slog.Info("smtp: server stopped")
}

// limitListener enforces the connection limiter per connection: connections
// over the per-IP or global limit are closed before any SMTP handling, and the
// slot is released when the connection closes. Accepted connections are
// additionally wrapped with the anti-slowloris transfer-rate tracker (armed by
// the session only during message-body transfer).
//
// Admission is deliberately NOT performed in Accept. Resolving the peer IP
// requires RemoteAddr(), which behind PROXY protocol blocks until the PROXY
// header is read (go-proxyproto waits up to its header timeout, 10s by
// default). Doing that in the accept loop would serialize every slow or
// malicious client through it, stalling acceptance of all new connections — a
// DoS. Instead Accept hands off promptly and limitedConn admits the connection
// lazily on its own goroutine, at the first read/write.
type limitListener struct {
	net.Listener
	limiter        *connlimiter.Limiter
	transferPolicy transferRatePolicy
}

func (l *limitListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	lc := &limitedConn{Conn: conn, limiter: l.limiter}
	// Rate tracker outermost so a policy drop closes through limitedConn
	// and releases the limiter slot.
	return newTransferRateConn(lc, l.transferPolicy), nil
}

// limitedConn defers connection-limiter admission to the connection's own
// goroutine and releases its slot exactly once on Close. Admission resolves the
// peer IP — which, behind PROXY protocol, blocks until the PROXY header is read
// — and then applies the limiter. A rejected connection is closed and every I/O
// returns net.ErrClosed, so go-smtp drops it without a greeting, exactly as the
// old accept-time rejection did. Because the blocking IP resolution runs here
// (first read/write) and never on the accept loop, one slow client cannot stall
// acceptance of other connections.
type limitedConn struct {
	net.Conn
	limiter *connlimiter.Limiter

	admitOnce sync.Once

	mu       sync.Mutex
	ip       string // resolved peer IP, set by admit
	admitErr error  // non-nil once admission rejected the connection
	slotHeld bool   // a limiter slot is currently held
	released bool   // the held slot has been released
	closed   bool   // Close has been called
}

// admit resolves the peer IP and applies the connection limiter exactly once,
// on the first read/write (the connection's own goroutine). It returns a
// non-nil error when the connection was rejected. The blocking RemoteAddr call
// is made without the mutex held so a concurrent Close never waits on the PROXY
// header read.
func (c *limitedConn) admit() error {
	c.admitOnce.Do(func() {
		ip := extractIP(c.Conn.RemoteAddr().String())
		granted := c.limiter.Accept(ip)

		c.mu.Lock()
		c.ip = ip
		switch {
		case !granted:
			c.admitErr = net.ErrClosed
			c.mu.Unlock()
			slog.Warn("smtp: connection rejected by limiter", "ip", ip)
			_ = c.Conn.Close()
		case c.closed:
			// Close raced ahead of admission; release the slot we just took so
			// it is not leaked (Close saw slotHeld=false and released nothing).
			c.released = true
			c.mu.Unlock()
			c.limiter.Release(ip)
		default:
			c.slotHeld = true
			c.mu.Unlock()
		}
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.admitErr
}

func (c *limitedConn) Read(b []byte) (int, error) {
	if err := c.admit(); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func (c *limitedConn) Write(b []byte) (int, error) {
	if err := c.admit(); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

func (c *limitedConn) Close() error {
	c.mu.Lock()
	c.closed = true
	release := c.slotHeld && !c.released
	if release {
		c.released = true
	}
	ip := c.ip
	c.mu.Unlock()
	if release {
		c.limiter.Release(ip)
	}
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
