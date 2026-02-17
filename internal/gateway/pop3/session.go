package pop3

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// Session represents a single POP3 conversation with a client.
type Session struct {
	conn      net.Conn
	reader    *bufio.Reader
	writer    *bufio.Writer
	api       *apiclient.Client
	hostname  string
	tlsConfig *tls.Config

	// Session state
	tls_      bool
	auth      *authState
	messages  []apiclient.MessageSummary
	deleted   map[int]bool // sequence numbers marked for deletion
}

type authState struct {
	authenticated bool
	email         string
	token         string
	accountID     uint
	username      string // stored between USER and PASS
}

// NewSession creates a new POP3 session.
func NewSession(conn net.Conn, api *apiclient.Client, hostname string, tlsConfig *tls.Config) *Session {
	return &Session{
		conn:      conn,
		reader:    bufio.NewReader(conn),
		writer:    bufio.NewWriter(conn),
		api:       api,
		hostname:  hostname,
		tlsConfig: tlsConfig,
		auth:      &authState{},
		deleted:   make(map[int]bool),
	}
}

// Handle runs the POP3 state machine.
func (s *Session) Handle() {
	defer s.conn.Close()

	slog.Info("pop3: new connection", "remote", s.conn.RemoteAddr())

	// Send greeting
	s.ok("RestMail POP3 server ready")

	for {
		s.conn.SetDeadline(time.Now().Add(10 * time.Minute))

		line, err := s.reader.ReadString('\n')
		if err != nil {
			slog.Debug("pop3: connection closed", "remote", s.conn.RemoteAddr(), "error", err)
			return
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}

		slog.Debug("pop3: recv", "remote", s.conn.RemoteAddr(), "cmd", line)

		cmd, arg := parseCommand(line)

		switch cmd {
		case "CAPA":
			s.handleCapa()
		case "STLS":
			if s.handleSTLS() {
				return
			}
		case "USER":
			s.handleUser(arg)
		case "PASS":
			s.handlePass(arg)
		case "STAT":
			s.handleStat()
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
			s.ok("")
		case "RSET":
			s.handleRset()
		case "QUIT":
			s.handleQuit()
			return
		default:
			s.err("Unknown command")
		}
	}
}

func (s *Session) handleCapa() {
	s.ok("Capability list follows")
	s.sendLine("USER")
	if !s.tls_ && s.tlsConfig != nil {
		s.sendLine("STLS")
	}
	s.sendLine("TOP")
	s.sendLine("UIDL")
	s.sendLine("RESP-CODES")
	s.sendLine("PIPELINING")
	s.sendLine(".")
}

func (s *Session) handleSTLS() bool {
	if s.tls_ {
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
	s.reader = bufio.NewReader(tlsConn)
	s.writer = bufio.NewWriter(tlsConn)
	s.tls_ = true

	slog.Info("pop3: TLS established", "remote", s.conn.RemoteAddr())
	return false
}

func (s *Session) handleUser(arg string) {
	if s.auth.authenticated {
		s.err("Already authenticated")
		return
	}
	if !s.tls_ && s.tlsConfig != nil {
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
	if s.auth.authenticated {
		s.err("Already authenticated")
		return
	}
	if s.auth.username == "" {
		s.err("USER first")
		return
	}
	if arg == "" {
		s.err("Password required")
		return
	}

	resp, err := s.api.Login(s.auth.username, arg)
	if err != nil {
		slog.Warn("pop3: auth failed",
			"remote", s.conn.RemoteAddr(),
			"user", s.auth.username,
			"event", "pop3_auth_failed",
		)
		s.err("[AUTH] Authentication failed")
		s.auth.username = "" // reset
		return
	}

	s.auth.authenticated = true
	s.auth.email = s.auth.username
	s.auth.token = resp.Data.AccessToken
	s.auth.accountID = resp.Data.User.ID

	// Load INBOX messages
	msgResp, err := s.api.ListMessages(s.auth.token, s.auth.accountID, "INBOX")
	if err != nil {
		slog.Error("pop3: failed to load inbox", "error", err)
		s.err("Failed to load mailbox")
		return
	}
	s.messages = msgResp.Data

	slog.Info("pop3: authenticated", "remote", s.conn.RemoteAddr(), "user", s.auth.username, "messages", len(s.messages))
	s.ok("Authentication successful")
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
			totalSize += msg.SizeBytes
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
		s.ok("%d %d", n, s.messages[n-1].SizeBytes)
		return
	}

	// All messages
	count := 0
	var totalSize int
	for i, msg := range s.messages {
		if !s.deleted[i+1] {
			count++
			totalSize += msg.SizeBytes
		}
	}

	s.ok("%d messages (%d octets)", count, totalSize)
	for i, msg := range s.messages {
		if !s.deleted[i+1] {
			s.sendLine("%d %d", i+1, msg.SizeBytes)
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
		s.ok("%d %d", n, s.messages[n-1].ID)
		return
	}

	// All messages
	s.ok("")
	for i, msg := range s.messages {
		if !s.deleted[i+1] {
			s.sendLine("%d %d", i+1, msg.ID)
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

	// Get full message from API
	detail, err := s.api.GetMessage(s.auth.token, msg.ID)
	if err != nil {
		s.err("Failed to retrieve message")
		return
	}

	raw := buildRawMessage(detail.Data)

	s.ok("%d octets", len(raw))
	// Send message, byte-stuffing lines starting with "."
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(line, ".") {
			s.sendLine(".%s", line)
		} else {
			s.sendLine("%s", line)
		}
	}
	s.sendLine(".")

	// Mark as read
	if !msg.IsRead {
		s.api.UpdateMessage(s.auth.token, msg.ID, map[string]interface{}{"is_read": true})
		s.messages[n-1].IsRead = true
	}
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
	detail, err := s.api.GetMessage(s.auth.token, msg.ID)
	if err != nil {
		s.err("Failed to retrieve message")
		return
	}

	raw := buildRawMessage(detail.Data)

	// Split into headers and body
	headerEnd := strings.Index(raw, "\r\n\r\n")
	if headerEnd == -1 {
		headerEnd = len(raw)
	}

	s.ok("")
	// Send headers
	headers := raw[:headerEnd]
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(line, ".") {
			s.sendLine(".%s", line)
		} else {
			s.sendLine("%s", line)
		}
	}
	s.sendLine("") // blank line separating headers from body

	// Send requested number of body lines
	if headerEnd+4 <= len(raw) {
		body := raw[headerEnd+4:] // skip \r\n\r\n
		bodyLines := strings.Split(body, "\r\n")
		if lines > len(bodyLines) {
			lines = len(bodyLines)
		}
		for i := 0; i < lines; i++ {
			if strings.HasPrefix(bodyLines[i], ".") {
				s.sendLine(".%s", bodyLines[i])
			} else {
				s.sendLine("%s", bodyLines[i])
			}
		}
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

func (s *Session) handleRset() {
	s.deleted = make(map[int]bool)
	s.ok("Maildrop has %d messages", len(s.messages))
}

func (s *Session) handleQuit() {
	// Actually delete marked messages
	for n := range s.deleted {
		if n >= 1 && n <= len(s.messages) {
			msg := s.messages[n-1]
			if err := s.api.DeleteMessage(s.auth.token, msg.ID); err != nil {
				slog.Error("pop3: failed to delete message", "id", msg.ID, "error", err)
			}
		}
	}

	s.ok("RestMail POP3 server signing off")
}

// ── Output helpers ────────────────────────────────────────────────────

func (s *Session) ok(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if msg != "" {
		fmt.Fprintf(s.writer, "+OK %s\r\n", msg)
	} else {
		fmt.Fprintf(s.writer, "+OK\r\n")
	}
	s.writer.Flush()
}

func (s *Session) err(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(s.writer, "-ERR %s\r\n", msg)
	s.writer.Flush()
}

func (s *Session) sendLine(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(s.writer, "%s\r\n", msg)
	s.writer.Flush()
}
