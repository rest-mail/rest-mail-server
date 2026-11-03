package server

import (
	"bufio"
	"crypto/tls"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

func (s *Session) handleSTARTTLS(tag string) bool {
	if s.usingTLS {
		s.tagged(tag, "BAD", "Already in TLS mode")
		return false
	}
	if s.tlsConfig == nil {
		s.tagged(tag, "BAD", "TLS not available")
		return false
	}

	s.tagged(tag, "OK", "Begin TLS negotiation now")

	tlsConn := tls.Server(s.conn, s.tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		slog.Warn("imap: TLS handshake failed", "error", err)
		return true
	}

	s.conn = tlsConn
	// SECURITY: install a FRESH reader over the TLS conn, discarding anything the
	// cleartext bufio.Reader had already buffered from the socket. A man-in-the-
	// middle can pipeline plaintext IMAP commands after STARTTLS in the same
	// segment; preserving that buffer would run them as though they arrived inside
	// the TLS session (plaintext injection, CVE-2011-0411 class). A conforming
	// client sends nothing until it sees this tagged OK, so no legitimate data is
	// lost by discarding.
	s.reader = bufio.NewReader(tlsConn)
	s.writer = bufio.NewWriter(tlsConn)
	s.usingTLS = true

	slog.Info("imap: TLS established", "remote", s.conn.RemoteAddr())
	return false
}

func (s *Session) handleLogin(tag, args string) {
	if s.refuseReauthentication(tag, "LOGIN") {
		return
	}
	// Enforce the same policy the greeting advertises via LOGINDISABLED: refuse
	// plaintext LOGIN until TLS is established (RFC 3501 §7.1.1, RFC 2595).
	if s.loginDisabled() {
		s.tagged(tag, "NO", "[PRIVACYREQUIRED] STARTTLS required")
		return
	}

	username, password, ok := wire.ParseLoginArgs(args)
	if !ok {
		s.tagged(tag, "BAD", "LOGIN requires username and password")
		return
	}

	s.authenticate(tag, username, password, "LOGIN completed")
}

// refuseReauthentication answers BAD to LOGIN or AUTHENTICATE once the session
// is authenticated: both are valid only in the not authenticated state, and a
// session cannot return to it or switch user (RFC 3501 §6.2). The rejected
// command changes no state and checks no credentials.
func (s *Session) refuseReauthentication(tag, cmd string) bool {
	if !s.auth.authenticated {
		return false
	}
	s.tagged(tag, "BAD", cmd+" not permitted: already authenticated")
	return true
}

func (s *Session) handleAuthenticate(tag, args string) {
	if s.refuseReauthentication(tag, "AUTHENTICATE") {
		return
	}
	// A missing mechanism name is an invalid argument: BAD, not the NO an
	// unsupported mechanism gets (RFC 3501 §6.2.2).
	if strings.TrimSpace(args) == "" {
		s.tagged(tag, "BAD", "AUTHENTICATE requires a mechanism name")
		return
	}

	// Gate cleartext SASL the same way LOGIN is gated: when TLS is configured
	// but not yet active, refuse before soliciting any credential so the base64
	// PLAIN response is never sent over the wire (RFC 3501 / RFC 2595).
	if s.loginDisabled() {
		s.tagged(tag, "NO", "[PRIVACYREQUIRED] STARTTLS required")
		return
	}

	// "AUTHENTICATE" SP auth-type [SP (base64 / "=")] (RFC 4959 §7). Only
	// PLAIN is supported.
	mech, initial, hasInitial := strings.Cut(args, " ")
	if !strings.EqualFold(mech, "PLAIN") {
		s.tagged(tag, "NO", "Unsupported mechanism")
		return
	}
	var line string
	if hasInitial {
		// SASL-IR: the initial client response is on the command line, so no
		// continuation is sent. "=" is a zero-length response; an empty or
		// space-separated one is not base64 / "=" at all.
		if initial == "" || strings.Contains(initial, " ") {
			s.tagged(tag, "BAD", "Invalid initial response")
			return
		}
		if initial != "=" {
			line = initial
		}
	} else {
		// Continuation request: "+" SP [base64-challenge] (RFC 3501 §§6.2.2,
		// 7.5). PLAIN carries no server challenge, so the base64 part is empty
		// and the line is "+ " (note the required trailing space after the "+").
		s.send("+ ")
		var tooLong bool
		var err error
		line, tooLong, err = s.readLine()
		if err != nil {
			return
		}
		if tooLong {
			s.tagged(tag, "BAD", "Authentication response line too long")
			return
		}
		// A lone "*" cancels the exchange, and the server MUST reject the
		// AUTHENTICATE command with a tagged BAD (RFC 3501 §6.2.2), not decode
		// it as base64 and report a NO.
		if line == "*" {
			s.tagged(tag, "BAD", "authentication aborted")
			return
		}
	}
	// An initial response's decoding errors are handled as any client
	// response's (RFC 4959 §3).
	decoded, err := wire.DecodeBase64(line)
	if err != nil || strings.ContainsAny(line, "\r\n") {
		s.tagged(tag, "NO", "Invalid base64")
		return
	}
	// RFC 4616 section 2: exactly two NUL delimiters, nonempty authcid and
	// password, and UTF-8 throughout. SplitN would admit a NUL in the password.
	// Validate before calling even a permissive authentication backend; string
	// preparation and comparison against stored credentials remain its job.
	parts := strings.Split(string(decoded), "\x00")
	if len(parts) != 3 || !utf8.Valid(decoded) || parts[1] == "" || parts[2] == "" {
		s.tagged(tag, "NO", "Invalid PLAIN data")
		return
	}

	// The authorization identity names the user whose privileges the client
	// requests (RFC 3501 §6.2.2). Acting as another user is not supported, so
	// an authzid other than the authentication identity is refused rather
	// than silently ignored (RFC 4616 §2).
	if parts[0] != "" && parts[0] != parts[1] {
		s.tagged(tag, "NO", "Authorization identity not permitted")
		return
	}

	s.authenticate(tag, parts[1], parts[2], "AUTHENTICATE completed")
}

// authenticate runs the shared LOGIN/AUTHENTICATE credential check, wiring the
// resulting Mailbox and applying limiter accounting. okMsg is the tagged OK text
// sent on success; a [CAPABILITY ...] response code carrying the post-auth
// capability list (which reflects the concrete Mailbox's optional interfaces) is
// prepended so clients see extensions like UIDPLUS without re-probing.
func (s *Session) authenticate(tag, username, password, okMsg string) {
	ip := extractIP(s.conn.RemoteAddr().String())

	mailbox, err := s.backend.Authenticate(username, password)
	if err != nil {
		slog.Warn("imap: auth failed",
			"remote", s.conn.RemoteAddr(),
			"user", username,
			"event", "imap_auth_failed",
			"ip", ip,
		)
		s.limiter.RecordAuthFail(ip)
		if s.limiter.IsBanned(ip) {
			s.tagged(tag, "NO", "Too many authentication failures")
			_ = s.conn.Close()
			return
		}
		s.tagged(tag, "NO", "[AUTHENTICATIONFAILED] Invalid credentials")
		return
	}

	s.limiter.ResetAuth(ip)
	s.auth.authenticated = true
	s.mailbox = mailbox

	slog.Info("imap: authenticated", "remote", s.conn.RemoteAddr(), "user", username)
	s.tagged(tag, "OK", "[CAPABILITY "+s.capabilities()+"] "+okMsg)
}
