package server

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rest-mail/go-pop3/pkg/pop3"
	"github.com/rest-mail/go-pop3/pkg/pop3/internal/wire"
)

// maxCommandLine bounds a single client command line, including its CRLF. RFC
// 1939 §3 keeps commands tiny (a keyword plus short arguments), so this is far
// more than any legitimate line yet small enough that a pre-authentication
// client cannot exhaust memory by streaming an unterminated line. The command
// reader's buffer is sized to this cap; see [Session.readCommandLine].
const maxCommandLine = 1024

// maxSASLResponseLine bounds the AUTH response line read after the "+ "
// challenge, including its CRLF. RFC 5034 §4 lifts the command-line limit for
// that line: a server MUST handle the largest response its mechanisms
// generate. For PLAIN, RFC 4616 §2 requires authzid, authcid and passwd of up
// to 255 octets each, so the message is at most 767 octets: 1024 base64
// characters plus CRLF.
const maxSASLResponseLine = 4*((3*255+2+2)/3) + 2

// errLineTooLong reports that a command line exceeded [maxCommandLine] bytes
// without a terminating LF.
var errLineTooLong = errors.New("command line too long")

// Session represents a single POP3 conversation with a client.
type Session struct {
	conn      net.Conn
	reader    *bufio.Reader
	writer    *bufio.Writer
	backend   pop3.Backend
	tlsConfig *tls.Config
	limiter   pop3.Limiter

	// Session state
	usingTLS bool
	utf8     bool // client issued UTF8 (RFC 6856); reset by STLS
	auth     *authState
	mailbox  pop3.Mailbox
	messages []pop3.Message
	deleted  map[int]bool // sequence numbers marked for deletion
}

type authState struct {
	authenticated bool
	username      string // pending only until the next command
}

// NewSession creates a POP3 session over conn, authenticating against backend.
// A nil limiter defaults to [pop3.NopLimiter]. Call [Session.Handle] to run it.
func NewSession(conn net.Conn, backend pop3.Backend, tlsConfig *tls.Config, limiter pop3.Limiter) *Session {
	if limiter == nil {
		limiter = pop3.NopLimiter{}
	}
	return &Session{
		conn:      conn,
		reader:    bufio.NewReaderSize(conn, maxCommandLine),
		writer:    bufio.NewWriter(conn),
		backend:   backend,
		tlsConfig: tlsConfig,
		limiter:   limiter,
		auth:      &authState{},
		deleted:   make(map[int]bool),
	}
}

// Handle runs the POP3 state machine until the client disconnects or QUITs.
//
// A panic in any command handler or Backend/Mailbox call is recovered here so a
// single misbehaving session is isolated — logged and answered with -ERR before
// the connection is closed — rather than unwinding the per-connection goroutine
// and crashing the whole process along with every concurrent session.
func (s *Session) Handle() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("pop3: session panic recovered",
				"remote", s.conn.RemoteAddr(),
				"panic", r,
				"stack", string(debug.Stack()),
			)
			// Best-effort notice to the client; the deferred Close follows.
			s.err("Internal server error")
		}
		_ = s.conn.Close()
	}()

	slog.Info("pop3: new connection", "remote", s.conn.RemoteAddr())

	// Send greeting
	s.ok("POP3 server ready")

	for {
		_ = s.conn.SetDeadline(time.Now().Add(10 * time.Minute))

		line, err := s.readCommandLine()
		if err != nil {
			if errors.Is(err, errLineTooLong) {
				// A client streamed a command line past the cap (likely hostile,
				// possibly pre-auth). Refuse it and drop the connection rather than
				// attempt to resynchronize on a line we deliberately truncated.
				slog.Warn("pop3: command line too long",
					"remote", s.conn.RemoteAddr(),
					"limit", maxCommandLine,
				)
				s.err("Command line too long")
				return
			}
			slog.Debug("pop3: connection closed", "remote", s.conn.RemoteAddr(), "error", err)
			return
		}

		line = strings.TrimRight(line, "\r\n")

		slog.Debug("pop3: recv", "remote", s.conn.RemoteAddr(), "cmd", wire.RedactCommand(line))

		cmd, arg := wire.ParseCommand(line)
		// RFC 1939 §7: PASS must immediately follow a successful USER.
		// Clear before dispatch so even refused commands and malformed USER
		// invalidate the previous exchange, including an empty command line.
		if cmd != "PASS" {
			s.auth.username = ""
		}

		switch cmd {
		case "CAPA":
			if !s.rejectArg(arg) {
				s.handleCapa()
			}
		case "STLS":
			if !s.rejectArg(arg) && s.handleSTLS() {
				return
			}
		case "UTF8":
			if !s.rejectArg(arg) {
				s.handleUTF8()
			}
		case "USER":
			s.handleUser(arg)
		case "PASS":
			s.handlePass(arg)
		case "AUTH":
			if s.handleAuth(arg) {
				return
			}
		case "STAT":
			if !s.rejectArg(arg) {
				s.handleStat()
			}
		case "LIST":
			s.handleList(arg)
		case "UIDL":
			s.handleUidl(arg)
		case "RETR":
			s.handleRetr(arg)
		case "TOP":
			s.handleTop(arg)
		case "DELE":
			s.handleDele(arg)
		case "NOOP":
			if !s.rejectArg(arg) {
				s.handleNoop()
			}
		case "RSET":
			if !s.rejectArg(arg) {
				s.handleRset()
			}
		case "QUIT":
			// A spurious argument makes QUIT a syntax error: reject it and stay in
			// the current state rather than entering the UPDATE phase, so a client
			// that mistyped the line does not unintentionally commit deletions.
			if !s.rejectArg(arg) {
				s.handleQuit()
				return
			}
		default:
			s.err("Unknown command")
		}
	}
}

// readCommandLine reads one LF-terminated command line, refusing to buffer more
// than [maxCommandLine] bytes. The underlying [bufio.Reader] is sized to that
// cap, so ReadSlice returns [bufio.ErrBufferFull] the instant a line fills the
// buffer without an LF — the overflow signal we surface as [errLineTooLong]
// instead of letting an unterminated line grow without limit. The returned slice
// aliases the reader's buffer, so it is copied into a string before returning.
func (s *Session) readCommandLine() (string, error) {
	line, err := s.reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", errLineTooLong
	}
	if err != nil {
		return "", err
	}
	return string(line), nil
}

// readSASLResponseLine reads the AUTH response line that follows a "+ "
// challenge, refusing more than [maxSASLResponseLine] bytes. It continues past
// the reader's [maxCommandLine] buffer, so memory stays bounded by the two caps
// together.
func (s *Session) readSASLResponseLine() (string, error) {
	var line []byte
	for {
		chunk, err := s.reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxSASLResponseLine {
			return "", errLineTooLong
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return "", err
		}
		return string(line), nil
	}
}

func (s *Session) handleCapa() {
	s.ok("Capability list follows")
	s.sendLine("USER")
	if s.saslOffered() {
		s.sendLine("SASL PLAIN")
	}
	if !s.usingTLS && s.tlsConfig != nil {
		s.sendLine("STLS")
	}
	s.sendLine("TOP")
	s.sendLine("UIDL")
	s.sendLine("RESP-CODES")
	s.sendLine("AUTH-RESP-CODE")
	s.sendLine("PIPELINING")
	if tag, ok := s.loginDelayTag(); ok {
		s.sendLine("%s", tag)
	}
	if tag, ok := s.implementationTag(); ok {
		s.sendLine("%s", tag)
	}
	if _, ok := s.backend.(pop3.UTF8Backend); ok {
		s.sendLine("UTF8")
	}
	s.sendLine(".")
}

// loginDelayTag builds the LOGIN-DELAY capability line (RFC 2449 §6.5) for the
// current state. AUTHORIZATION announces the Backend's minimum, followed by USER
// when the delay varies per user; TRANSACTION announces the user's own value
// when the Mailbox reports one.
func (s *Session) loginDelayTag() (string, bool) {
	b, ok := s.backend.(pop3.LoginDelayBackend)
	if !ok {
		return "", false
	}
	seconds, perUser := b.LoginDelay()
	if s.mailbox != nil {
		perUser = false
		if m, ok := s.mailbox.(pop3.LoginDelayMailbox); ok {
			seconds = m.LoginDelay()
		}
	}
	if seconds < 0 {
		return "", false
	}
	tag := "LOGIN-DELAY " + strconv.Itoa(seconds)
	if perUser {
		tag += " USER"
	}
	return tag, true
}

// implementationTag builds the IMPLEMENTATION capability line (RFC 2449 §6.9)
// for the current state. It is omitted before login when the Backend asks for
// TRANSACTION state only, and whenever the text is not a valid §9 param list or
// the line would exceed the 512-octet response limit (§4).
func (s *Session) implementationTag() (string, bool) {
	b, ok := s.backend.(pop3.ImplementationBackend)
	if !ok {
		return "", false
	}
	text, transactionOnly := b.Implementation()
	if transactionOnly && s.mailbox == nil {
		return "", false
	}
	tag := "IMPLEMENTATION " + text
	if len(tag)+len("\r\n") > 512 {
		return "", false
	}
	for _, param := range strings.Split(text, " ") {
		if param == "" {
			return "", false
		}
		for i := 0; i < len(param); i++ {
			if param[i] < 0x21 || param[i] > 0x7e {
				return "", false
			}
		}
	}
	return tag, true
}

// handleUTF8 enables UTF-8 mode (RFC 6856 §3.1). The command is valid only in
// AUTHORIZATION state, and only when the Backend can serve both forms of a
// message; otherwise it is as unknown as before the extension existed.
func (s *Session) handleUTF8() {
	if _, ok := s.backend.(pop3.UTF8Backend); !ok {
		s.err("Unknown command")
		return
	}
	if s.auth.authenticated {
		s.err("Command only valid before authentication")
		return
	}
	s.utf8 = true
	s.ok("UTF8 enabled")
}

func (s *Session) handleSTLS() bool {
	// STLS belongs to the AUTHORIZATION state (RFC 2595 §3): it may be issued only
	// before authentication, so the USER/PASS exchange that follows is protected by
	// the freshly negotiated TLS layer. Once the session has entered TRANSACTION
	// state the command is out of sequence — upgrading an already-authenticated
	// connection serves no purpose — and must be refused with -ERR regardless of
	// the current TLS status (issue #17). This state check precedes the usingTLS
	// check so an authenticated session gets the correct "out of sequence" reason
	// rather than an incidental "already using TLS".
	if s.auth.authenticated {
		s.err("Command only valid before authentication")
		return false
	}
	if s.usingTLS {
		s.err("Already using TLS")
		return false
	}
	if s.tlsConfig == nil {
		s.err("TLS not available")
		return false
	}

	s.ok("Begin TLS negotiation")

	tlsConn := tls.Server(s.conn, s.tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		slog.Warn("pop3: TLS handshake failed", "error", err)
		return true
	}

	s.conn = tlsConn
	s.reader = bufio.NewReaderSize(tlsConn, maxCommandLine)
	s.writer = bufio.NewWriter(tlsConn)
	s.usingTLS = true
	// Like the capability list (RFC 2595 §4), a mode the client chose before
	// TLS is not trusted afterwards: it must issue UTF8 again.
	s.utf8 = false

	slog.Info("pop3: TLS established", append([]any{"remote", s.conn.RemoteAddr()}, s.tlsLogAttrs()...)...)
	return false
}

func (s *Session) handleUser(arg string) {
	if s.auth.authenticated {
		s.err("Already authenticated")
		return
	}
	if !s.usingTLS && s.tlsConfig != nil {
		s.err("TLS required")
		return
	}
	if arg == "" {
		s.err("Username required")
		return
	}
	s.auth.username = arg
	s.ok("")
}

func (s *Session) handlePass(arg string) {
	// Consume once, including empty PASS and every backend failure path. Keep
	// the username locally for authentication and logs after clearing state.
	username := s.auth.username
	s.auth.username = ""
	if s.auth.authenticated {
		s.err("Already authenticated")
		return
	}
	if username == "" {
		s.err("USER first")
		return
	}
	if arg == "" {
		s.err("Password required")
		return
	}

	var mailbox pop3.Mailbox
	var err error
	if b, ok := s.backend.(pop3.UTF8Backend); ok {
		mailbox, err = b.AuthenticateUTF8(username, arg, s.utf8)
	} else {
		mailbox, err = s.backend.Authenticate(username, arg)
	}
	s.login(username, mailbox, err)
}

// saslOffered reports whether AUTH PLAIN is accepted now, which is when CAPA
// lists it: the Backend must be a [pop3.SASLBackend], and a configured TLS layer
// must be active, as for USER.
func (s *Session) saslOffered() bool {
	_, ok := s.backend.(pop3.SASLBackend)
	return ok && (s.usingTLS || s.tlsConfig == nil)
}

// handleAuth runs the RFC 5034 AUTH command with the only mechanism offered,
// SASL PLAIN (RFC 4616). The client response comes either as the initial
// response argument or on the line after an empty "+ " challenge. It reports
// whether the session must end because the response line could not be read.
func (s *Session) handleAuth(arg string) bool {
	b, ok := s.backend.(pop3.SASLBackend)
	if !ok {
		s.err("Unknown command")
		return false
	}
	if s.auth.authenticated {
		s.err("Already authenticated")
		return false
	}
	if !s.usingTLS && s.tlsConfig != nil {
		s.err("TLS required")
		return false
	}
	mechanism, response, initial := strings.Cut(arg, " ")
	if mechanism == "" {
		s.err("Mechanism required")
		return false
	}
	if wire.UpperASCII(mechanism) != "PLAIN" {
		s.err("Unsupported mechanism")
		return false
	}
	if !initial {
		s.write("+ \r\n")
		line, err := s.readSASLResponseLine()
		if errors.Is(err, errLineTooLong) {
			s.err("Command line too long")
			return true
		}
		if err != nil {
			return true
		}
		response = strings.TrimRight(line, "\r\n")
		// RFC 5034 §4: a lone "*" cancels the exchange.
		if response == "*" {
			s.err("Authentication cancelled")
			return false
		}
	}
	// RFC 5034 §4: an initial response of "=" is empty, and a response that
	// does not decode rejects the command.
	if response == "=" {
		response = ""
	}
	// encoding/base64 skips CR and LF, but RFC 5034 §4 says to reject any
	// character outside the base64 alphabet rather than ignore it.
	if strings.ContainsAny(response, "\r\n") {
		s.err("Invalid base64 response")
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(response)
	if err != nil {
		s.err("Invalid base64 response")
		return false
	}
	// RFC 4616 §2: [authzid] NUL authcid NUL passwd, all UTF-8, with a
	// non-empty authcid and passwd.
	fields := strings.Split(string(decoded), "\x00")
	if len(fields) != 3 || fields[1] == "" || fields[2] == "" || !utf8.Valid(decoded) {
		s.err("Invalid PLAIN response")
		return false
	}
	mailbox, err := b.AuthenticatePlain(fields[0], fields[1], fields[2], s.utf8)
	s.login(fields[1], mailbox, err)
	return false
}

// login finishes a USER/PASS or AUTH attempt with the Backend's answer: it
// replies with the right response code, keeps the [pop3.Limiter] up to date, and on
// success loads the maildrop and enters TRANSACTION.
func (s *Session) login(username string, mailbox pop3.Mailbox, err error) {
	ip := extractIP(s.conn.RemoteAddr().String())

	if errors.Is(err, pop3.ErrInUse) {
		// ErrInUse is a backend assertion that the credentials were valid;
		// contention must not earn an authentication-failure ban.
		s.limiter.ResetAuth(ip)
		s.err("[IN-USE] Maildrop already in use")
		return
	}
	if errors.Is(err, pop3.ErrLoginDelay) {
		// Like ErrInUse, the credentials were valid: logging in too soon is a
		// backend policy refusal, not an authentication failure.
		s.limiter.ResetAuth(ip)
		s.err("[LOGIN-DELAY] Logged in too recently")
		return
	}
	if errors.Is(err, pop3.ErrSystemTemporary) || errors.Is(err, pop3.ErrSystemPermanent) {
		// A system outage does not establish whether the credentials were
		// valid. Preserve their failure history without charging a failure.
		slog.Error("pop3: authentication system failure", "error", err)
		s.systemError(err, "Authentication unavailable")
		return
	}
	if err != nil {
		slog.Warn("pop3: auth failed", append([]any{
			"remote", s.conn.RemoteAddr(),
			"user", username,
			"event", "pop3_auth_failed",
			"ip", ip,
		}, s.tlsLogAttrs()...)...)
		s.limiter.RecordAuthFail(ip)
		if s.limiter.IsBanned(ip) {
			s.err("[AUTH] Too many authentication failures")
			_ = s.conn.Close()
			return
		}
		s.err("[AUTH] Authentication failed")
		return
	}

	s.limiter.ResetAuth(ip)

	// A backend that returns (nil, nil) authenticated the user but handed us no
	// mailbox. Reject it and stay in AUTHORIZATION rather than dereferencing a
	// nil Mailbox interface below. A backend bug will not heal on retry, so
	// the reply carries RFC 3206 §4 SYS/PERM rather than a credential code.
	if mailbox == nil {
		slog.Error("pop3: backend returned nil mailbox",
			"remote", s.conn.RemoteAddr(),
			"user", username,
		)
		s.err("[SYS/PERM] Failed to load mailbox")
		return
	}

	// Open the maildrop as part of entering TRANSACTION state (RFC 1939 §4). The
	// session must not be marked authenticated until the message list is loaded:
	// if the load fails after we flip the flag, the client is wedged in
	// TRANSACTION with a phantom empty maildrop and cannot re-authenticate.
	messages, err := mailbox.Messages()
	// The credentials were good, so this is not AUTH. A maildrop held by another
	// session is IN-USE (RFC 2449 §8.1.2); other errors use the backend
	// classification or default to SYS/TEMP (RFC 3206 §4).
	if err != nil {
		if errors.Is(err, pop3.ErrInUse) {
			s.err("[IN-USE] Maildrop already in use")
			return
		}
		slog.Error("pop3: failed to load mailbox", "error", err)
		s.systemError(err, "Failed to load mailbox")
		return
	}

	// The load succeeded: commit the session to TRANSACTION state atomically.
	s.auth.authenticated = true
	s.mailbox = mailbox
	s.messages = messages

	slog.Info("pop3: authenticated", append([]any{"remote", s.conn.RemoteAddr(), "user", username, "messages", len(s.messages)}, s.tlsLogAttrs()...)...)
	s.ok("Authentication successful")
}

// tlsLogAttrs names the TLS version and cipher suite for the STLS and login
// logs (RFC 8314 §4), or nothing on a plaintext connection.
func (s *Session) tlsLogAttrs() []any {
	c, ok := s.conn.(*tls.Conn)
	if !ok {
		return nil
	}
	state := c.ConnectionState()
	return []any{"tls_version", tls.VersionName(state.Version), "tls_cipher", tls.CipherSuiteName(state.CipherSuite)}
}

// systemError keeps backend details out of the wire response. Unclassified
// maildrop errors remain temporary; an explicit permanent error takes priority.
func (s *Session) systemError(err error, message string) {
	code := "SYS/TEMP"
	if errors.Is(err, pop3.ErrSystemPermanent) {
		code = "SYS/PERM"
	}
	s.err("[%s] %s", code, message)
}

func (s *Session) handleStat() {
	if !s.auth.authenticated {
		s.err("Not authenticated")
		return
	}

	count := 0
	var totalSize int
	for i, msg := range s.messages {
		if !s.deleted[i+1] {
			count++
			totalSize += msg.Size
		}
	}

	s.ok("%d %d", count, totalSize)
}

func (s *Session) handleList(arg string) {
	if !s.auth.authenticated {
		s.err("Not authenticated")
		return
	}

	if arg != "" {
		// Single message
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > len(s.messages) {
			s.err("No such message")
			return
		}
		if s.deleted[n] {
			s.err("Message is deleted")
			return
		}
		s.ok("%d %d", n, s.messages[n-1].Size)
		return
	}

	// All messages
	count := 0
	var totalSize int
	for i, msg := range s.messages {
		if !s.deleted[i+1] {
			count++
			totalSize += msg.Size
		}
	}

	s.ok("%d messages (%d octets)", count, totalSize)
	for i, msg := range s.messages {
		if !s.deleted[i+1] {
			s.sendLine("%d %d", i+1, msg.Size)
		}
	}
	s.sendLine(".")
}

func (s *Session) handleUidl(arg string) {
	if !s.auth.authenticated {
		s.err("Not authenticated")
		return
	}

	if arg != "" {
		// Single message
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > len(s.messages) {
			s.err("No such message")
			return
		}
		if s.deleted[n] {
			s.err("Message is deleted")
			return
		}
		uid := s.messages[n-1].UID
		// Fail safe on a non-conforming id (RFC 1939 §7): emitting it verbatim
		// would break the "n uid" line or inject protocol lines (CR/LF). See
		// [wire.ValidUID].
		if !wire.ValidUID(uid) {
			slog.Error("pop3: backend supplied invalid UIDL unique-id",
				"remote", s.conn.RemoteAddr(),
				"msg", n,
				"uidlen", len(uid),
			)
			s.err("Invalid unique-id for message")
			return
		}
		s.ok("%d %s", n, uid)
		return
	}

	// All messages. Validate the whole listing BEFORE writing the "+OK" header:
	// once the header is sent the reply is a dot-terminated block we cannot
	// cleanly abort, so a single malformed id emitted mid-list would corrupt the
	// framing. Pre-scan the non-deleted messages and reject the entire command
	// with -ERR if any id is malformed (RFC 1939 §7 grammar) or duplicated (§7
	// requires ids unique within the maildrop; a duplicate breaks leave-on-server
	// de-duplication).
	seen := make(map[string]bool, len(s.messages))
	for i, msg := range s.messages {
		if s.deleted[i+1] {
			continue
		}
		if !wire.ValidUID(msg.UID) {
			slog.Error("pop3: backend supplied invalid UIDL unique-id",
				"remote", s.conn.RemoteAddr(),
				"msg", i+1,
				"uidlen", len(msg.UID),
			)
			s.err("Invalid unique-id in maildrop")
			return
		}
		if seen[msg.UID] {
			slog.Error("pop3: duplicate UIDL unique-id in maildrop",
				"remote", s.conn.RemoteAddr(),
				"msg", i+1,
			)
			s.err("Duplicate unique-id in maildrop")
			return
		}
		seen[msg.UID] = true
	}

	s.ok("")
	for i, msg := range s.messages {
		if !s.deleted[i+1] {
			s.sendLine("%d %s", i+1, msg.UID)
		}
	}
	s.sendLine(".")
}

func (s *Session) handleRetr(arg string) {
	if !s.auth.authenticated {
		s.err("Not authenticated")
		return
	}

	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > len(s.messages) {
		s.err("No such message")
		return
	}
	if s.deleted[n] {
		s.err("Message is deleted")
		return
	}

	msg := s.messages[n-1]

	rawBytes, err := s.mailbox.Retrieve(msg.UID)
	if err != nil {
		s.retrieveFailed(err)
		return
	}
	// Canonicalize line endings to CRLF first: a bare LF in stored content must
	// not survive inside a line, or it would evade dot-stuffing and could forge
	// the "." terminator. An unterminated last line gets its CRLF here too, so
	// octets are counted on exactly the message the client decodes.
	raw := wire.WireMessage(string(rawBytes))

	// The advertised count is the RFC 1939 §11 octet count of the message on the
	// canonical CRLF form: len(raw) == OctetCount(rawBytes). This is the exact
	// value STAT/LIST report via Message.Size when the backend sizes messages with
	// OctetCount, so the two agree (issue #16).
	s.ok("%d octets", len(raw))
	// Send message, byte-stuffing lines starting with ".". wire.SplitMessageLines
	// drops the trailing "" that strings.Split leaves for the final line's CRLF,
	// so no spurious blank line is emitted before the "." terminator and the
	// transmitted octets match the advertised count (RFC 1939 §5.1/§11).
	for _, line := range wire.SplitMessageLines(raw) {
		s.sendStuffed(line)
	}
	s.sendLine(".")

	// Mark as read
	if !msg.Seen {
		_ = s.mailbox.MarkSeen(msg.UID)
		s.messages[n-1].Seen = true
	}
}

// retrieveFailed answers a RETR or TOP whose Retrieve failed. A message that
// needs UTF-8 mode gets [UTF8] (RFC 6856 §5) only when the client could enable
// that mode; any other error text stays private.
func (s *Session) retrieveFailed(err error) {
	if errors.Is(err, pop3.ErrUTF8Required) && !s.utf8 {
		if _, ok := s.backend.(pop3.UTF8Backend); ok {
			s.err("[UTF8] Message requires UTF-8 mode")
			return
		}
	}
	s.err("Failed to retrieve message")
}

func (s *Session) handleTop(arg string) {
	if !s.auth.authenticated {
		s.err("Not authenticated")
		return
	}

	parts := strings.SplitN(arg, " ", 2)
	if len(parts) < 2 {
		s.err("Syntax: TOP msg lines")
		return
	}

	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 1 || n > len(s.messages) {
		s.err("No such message")
		return
	}
	if s.deleted[n] {
		s.err("Message is deleted")
		return
	}

	lines, err := strconv.Atoi(parts[1])
	if err != nil || lines < 0 {
		s.err("Invalid line count")
		return
	}

	msg := s.messages[n-1]
	rawBytes, err := s.mailbox.Retrieve(msg.UID)
	if err != nil {
		s.retrieveFailed(err)
		return
	}
	// Canonicalize line endings to CRLF first, so a bare LF cannot hide a line
	// boundary from the header/body split or the per-line dot-stuffing below.
	raw := wire.CanonicalCRLF(string(rawBytes))

	s.ok("")
	// wire.TopLines splits headers from body at the first empty line, emits the blank
	// separator, and caps the body at the requested count. Its lines come from
	// wire.SplitMessageLines, so no phantom or trailing blank line is emitted (§7).
	for _, line := range wire.TopLines(raw, lines) {
		s.sendStuffed(line)
	}
	s.sendLine(".")
}

func (s *Session) handleDele(arg string) {
	if !s.auth.authenticated {
		s.err("Not authenticated")
		return
	}

	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > len(s.messages) {
		s.err("No such message")
		return
	}
	if s.deleted[n] {
		s.err("Message already deleted")
		return
	}

	s.deleted[n] = true
	s.ok("Message %d deleted", n)
}

// handleNoop answers NOOP. NOOP is a TRANSACTION-state command (RFC 1939 §5):
// like every other post-authentication command it is refused with -ERR while the
// session is still in AUTHORIZATION (issue #11).
func (s *Session) handleNoop() {
	if !s.auth.authenticated {
		s.err("Not authenticated")
		return
	}
	s.ok("")
}

func (s *Session) handleRset() {
	// RSET resets the maildrop and is only meaningful once a maildrop is open, so
	// it belongs to the TRANSACTION state (RFC 1939 §5). Reject it before
	// authentication rather than reporting +OK on a maildrop that does not yet
	// exist (issue #11).
	if !s.auth.authenticated {
		s.err("Not authenticated")
		return
	}
	s.deleted = make(map[int]bool)
	s.ok("Maildrop has %d messages", len(s.messages))
}

func (s *Session) handleQuit() {
	// Enter the UPDATE state (RFC 1939 §6): commit every message the client
	// marked for deletion. If any commit fails, the maildrop update did not fully
	// succeed, so we must NOT report +OK — a +OK tells the client its DELEtes are
	// durable and it may drop local copies, silently losing mail that is still on
	// the server (and leaving UIDLs the client believes are gone). Attempt every
	// deletion regardless, then answer -ERR when any of them failed.
	failed := false
	for n := range s.deleted {
		if n >= 1 && n <= len(s.messages) {
			msg := s.messages[n-1]
			if err := s.mailbox.Delete(msg.UID); err != nil {
				slog.Error("pop3: failed to delete message", "uid", msg.UID, "error", err)
				failed = true
			}
		}
	}

	if failed {
		s.err("some deleted messages not removed")
		return
	}

	s.ok("POP3 server signing off")
}

// rejectArg reports whether a no-argument command carried an unexpected
// argument. STAT, RSET, NOOP, and QUIT (RFC 1939 §3), together with STLS
// (RFC 2595) and CAPA (RFC 2449), are defined to take no arguments, so a
// trailing token makes the command line a syntax error that must be answered
// -ERR rather than silently ignored (issue #14). When arg is non-empty this
// writes the -ERR reply and returns true; the caller then skips the handler and
// preserves transaction state. Dispatch still invalidates pending USER.
// An empty arg returns false so the handler runs normally. Only a
// non-whitespace token counts as an argument, so a stray
// trailing space is tolerated rather than treated as a syntax error.
func (s *Session) rejectArg(arg string) bool {
	if strings.TrimSpace(arg) != "" {
		s.err("Command accepts no arguments")
		return true
	}
	return false
}

// ── Output helpers ────────────────────────────────────────────────────

func (s *Session) ok(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if msg != "" {
		s.write("+OK " + msg + "\r\n")
	} else {
		s.write("+OK\r\n")
	}
}

func (s *Session) err(format string, args ...interface{}) {
	s.write("-ERR " + fmt.Sprintf(format, args...) + "\r\n")
}

func (s *Session) sendLine(format string, args ...interface{}) {
	s.write(fmt.Sprintf(format, args...) + "\r\n")
}

// write emits a pre-formatted line to the client and flushes. Protocol writes
// are best-effort: a failure surfaces on the next read, so the error is ignored.
func (s *Session) write(line string) {
	_, _ = s.writer.WriteString(line)
	_ = s.writer.Flush()
}

// sendStuffed writes one line of message content with POP3 byte-stuffing
// (RFC 1939): a line beginning with "." gets an extra leading "." so it is not
// mistaken for the "." terminator.
func (s *Session) sendStuffed(line string) {
	s.sendLine("%s", wire.DotStuff(line))
}
