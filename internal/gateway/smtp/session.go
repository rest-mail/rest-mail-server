package smtp

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
	"gorm.io/gorm"
)

// Session represents a single SMTP conversation with a client.
type Session struct {
	conn       net.Conn
	reader     *bufio.Reader
	writer     *bufio.Writer
	api        *apiclient.Client
	hostname   string
	remoteAddr string
	tlsConfig  *tls.Config
	db         *gorm.DB
	limiter    *connlimiter.Limiter

	// Session state
	heloName   string
	mailFrom   string
	rcptTo     []string
	data       []byte
	tls_       bool
	auth       *authState
	isSubmission bool // port 587/465 requires AUTH
}

type authState struct {
	authenticated bool
	email         string
	token         string
	accountID     uint
}

// NewSession creates a new SMTP session.
func NewSession(conn net.Conn, api *apiclient.Client, hostname string, tlsConfig *tls.Config, db *gorm.DB, isSubmission bool, limiter *connlimiter.Limiter) *Session {
	return &Session{
		conn:         conn,
		reader:       bufio.NewReader(conn),
		writer:       bufio.NewWriter(conn),
		api:          api,
		hostname:     hostname,
		remoteAddr:   conn.RemoteAddr().String(),
		tlsConfig:    tlsConfig,
		db:           db,
		limiter:      limiter,
		isSubmission: isSubmission,
		auth:         &authState{},
	}
}

// Handle runs the SMTP state machine for this session.
func (s *Session) Handle() {
	defer s.conn.Close()

	slog.Info("smtp: new connection", "remote", s.remoteAddr, "submission", s.isSubmission)

	// Send greeting
	s.reply(220, "%s ESMTP RestMail", s.hostname)

	for {
		s.conn.SetDeadline(time.Now().Add(5 * time.Minute))

		line, err := s.reader.ReadString('\n')
		if err != nil {
			slog.Debug("smtp: connection read error", "remote", s.remoteAddr, "error", err)
			return
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}

		slog.Debug("smtp: recv", "remote", s.remoteAddr, "cmd", line)

		// Parse command and argument
		cmd, arg := parseCommand(line)

		switch cmd {
		case "HELO":
			s.handleHELO(arg)
		case "EHLO":
			s.handleEHLO(arg)
		case "STARTTLS":
			if s.handleSTARTTLS() {
				return // TLS upgrade resets session - new Handle() runs on TLS conn
			}
		case "AUTH":
			s.handleAUTH(arg)
		case "MAIL":
			s.handleMAIL(arg)
		case "RCPT":
			s.handleRCPT(arg)
		case "DATA":
			s.handleDATA()
		case "RSET":
			s.handleRSET()
		case "NOOP":
			s.reply(250, "OK")
		case "QUIT":
			s.reply(221, "Bye")
			return
		case "VRFY":
			s.reply(252, "Cannot VRFY user, but will accept message and attempt delivery")
		default:
			s.reply(500, "Unrecognised command")
		}
	}
}

func (s *Session) handleHELO(arg string) {
	if arg == "" {
		s.reply(501, "HELO requires domain name")
		return
	}
	s.heloName = arg
	s.reply(250, "%s", s.hostname)
}

func (s *Session) handleEHLO(arg string) {
	if arg == "" {
		s.reply(501, "EHLO requires domain name")
		return
	}
	s.heloName = arg

	// Build capability list
	caps := []string{
		fmt.Sprintf("250-%s", s.hostname),
		"250-PIPELINING",
		"250-SIZE 10240000",
		"250-8BITMIME",
		"250-ENHANCEDSTATUSCODES",
	}

	// Advertise STARTTLS if not already TLS
	if !s.tls_ && s.tlsConfig != nil {
		caps = append(caps, "250-STARTTLS")
	}

	// Advertise AUTH on submission ports (only after TLS)
	if s.isSubmission && (s.tls_ || s.tlsConfig == nil) {
		caps = append(caps, "250-AUTH PLAIN LOGIN")
	}

	// Advertise RESTMAIL capability for server-to-server upgrade
	caps = append(caps, fmt.Sprintf("250-RESTMAIL https://%s/restmail", s.hostname))

	// Last capability without hyphen
	caps = append(caps, "250 OK")

	for _, cap := range caps {
		fmt.Fprintf(s.writer, "%s\r\n", cap)
	}
	s.writer.Flush()
}

func (s *Session) handleSTARTTLS() bool {
	if s.tls_ {
		s.reply(503, "Already in TLS mode")
		return false
	}
	if s.tlsConfig == nil {
		s.reply(454, "TLS not available")
		return false
	}

	s.reply(220, "Ready to start TLS")

	tlsConn := tls.Server(s.conn, s.tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		slog.Warn("smtp: TLS handshake failed", "remote", s.remoteAddr, "error", err)
		return true // connection is broken
	}

	// Replace conn with TLS conn and reset session state
	s.conn = tlsConn
	s.reader = bufio.NewReader(tlsConn)
	s.writer = bufio.NewWriter(tlsConn)
	s.tls_ = true
	s.heloName = ""
	s.mailFrom = ""
	s.rcptTo = nil

	slog.Info("smtp: TLS established", "remote", s.remoteAddr)
	return false
}

func (s *Session) handleAUTH(arg string) {
	if !s.isSubmission {
		s.reply(503, "AUTH not available on this port")
		return
	}
	if s.auth.authenticated {
		s.reply(503, "Already authenticated")
		return
	}
	if !s.tls_ && s.tlsConfig != nil {
		s.reply(538, "Encryption required for requested authentication mechanism")
		return
	}

	parts := strings.SplitN(arg, " ", 2)
	mechanism := strings.ToUpper(parts[0])

	switch mechanism {
	case "PLAIN":
		s.handleAuthPlain(parts)
	case "LOGIN":
		s.handleAuthLogin()
	default:
		s.reply(504, "Unrecognised authentication mechanism")
	}
}

func (s *Session) handleAuthPlain(parts []string) {
	var encoded string
	if len(parts) > 1 && parts[1] != "" {
		// Credentials inline: AUTH PLAIN <base64>
		encoded = parts[1]
	} else {
		// Credentials on next line
		s.reply(334, "")
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return
		}
		encoded = strings.TrimRight(line, "\r\n")
	}

	// Decode base64: \0user\0password
	decoded, err := decodeBase64(encoded)
	if err != nil {
		s.reply(535, "Authentication failed")
		return
	}

	parts2 := strings.SplitN(string(decoded), "\x00", 3)
	if len(parts2) != 3 {
		s.reply(535, "Authentication failed")
		return
	}

	// parts2[0] = authorization identity (unused), parts2[1] = username, parts2[2] = password
	username := parts2[1]
	password := parts2[2]

	s.doAuth(username, password)
}

func (s *Session) handleAuthLogin() {
	// Ask for username
	s.reply(334, "VXNlcm5hbWU6") // base64("Username:")
	userLine, err := s.reader.ReadString('\n')
	if err != nil {
		return
	}
	userBytes, err := decodeBase64(strings.TrimRight(userLine, "\r\n"))
	if err != nil {
		s.reply(535, "Authentication failed")
		return
	}

	// Ask for password
	s.reply(334, "UGFzc3dvcmQ6") // base64("Password:")
	passLine, err := s.reader.ReadString('\n')
	if err != nil {
		return
	}
	passBytes, err := decodeBase64(strings.TrimRight(passLine, "\r\n"))
	if err != nil {
		s.reply(535, "Authentication failed")
		return
	}

	s.doAuth(string(userBytes), string(passBytes))
}

func (s *Session) doAuth(username, password string) {
	ip := extractIP(s.remoteAddr)

	resp, err := s.api.Login(username, password)
	if err != nil {
		slog.Warn("smtp: auth failed",
			"remote", s.remoteAddr,
			"user", username,
			"event", "smtp_auth_failed",
			"ip", ip,
		)
		s.limiter.RecordAuthFail(ip)
		if s.limiter.IsBanned(ip) {
			s.reply(421, "Too many authentication failures")
			return
		}
		s.reply(535, "Authentication failed")
		return
	}

	s.limiter.ResetAuth(ip)
	s.auth.authenticated = true
	s.auth.email = username
	s.auth.token = resp.Data.AccessToken
	s.auth.accountID = resp.Data.User.ID

	slog.Info("smtp: authenticated", "remote", s.remoteAddr, "user", username)
	s.reply(235, "Authentication successful")
}

func (s *Session) handleMAIL(arg string) {
	if s.heloName == "" {
		s.reply(503, "EHLO/HELO first")
		return
	}
	if s.isSubmission && !s.auth.authenticated {
		s.reply(530, "Authentication required")
		return
	}

	from := extractAddress(arg, "FROM")
	if from == "" {
		s.reply(501, "Syntax: MAIL FROM:<address>")
		return
	}

	// On submission port, verify sender matches authenticated user or a linked account
	if s.isSubmission && s.auth.authenticated {
		if from != s.auth.email {
			// Check linked accounts
			var count int64
			s.db.Table("linked_accounts").
				Joins("JOIN mailboxes ON mailboxes.id = linked_accounts.mailbox_id").
				Where("linked_accounts.webmail_account_id = ? AND mailboxes.address = ?", s.auth.accountID, from).
				Count(&count)
			if count == 0 {
				slog.Warn("smtp: sender not authorized", "auth_user", s.auth.email, "mail_from", from)
				s.reply(553, "5.7.1 Sender address not authorized for this account")
				return
			}
		}
	}

	s.mailFrom = from
	s.rcptTo = nil
	s.data = nil
	s.reply(250, "OK")
}

func (s *Session) handleRCPT(arg string) {
	if s.mailFrom == "" {
		s.reply(503, "MAIL FROM first")
		return
	}

	to := extractAddress(arg, "TO")
	if to == "" {
		s.reply(501, "Syntax: RCPT TO:<address>")
		return
	}

	if len(s.rcptTo) >= 100 {
		s.reply(452, "Too many recipients")
		return
	}

	// Check if recipient is local by querying the API
	resp, err := s.api.CheckMailbox(to)
	if err != nil {
		// If API is unreachable, temp fail
		slog.Error("smtp: API error checking mailbox", "address", to, "error", err)
		s.reply(451, "Temporary service failure")
		return
	}

	if !resp.Data.Exists {
		// Not a local recipient — on inbound we reject, on submission we'll queue for outbound
		if s.isSubmission && s.auth.authenticated {
			// Submission: accept for outbound delivery (queue later)
			s.rcptTo = append(s.rcptTo, to)
			s.reply(250, "OK")
			return
		}
		s.reply(550, "No such user - %s", to)
		return
	}

	s.rcptTo = append(s.rcptTo, to)
	s.reply(250, "OK")
}

func (s *Session) handleDATA() {
	if len(s.rcptTo) == 0 {
		s.reply(503, "RCPT TO first")
		return
	}

	s.reply(354, "End data with <CR><LF>.<CR><LF>")

	// Read message data until lone "." on a line
	var data []byte
	for {
		s.conn.SetDeadline(time.Now().Add(10 * time.Minute))
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			slog.Error("smtp: error reading DATA", "remote", s.remoteAddr, "error", err)
			return
		}

		// Check for end-of-data marker
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "." {
			break
		}

		// Dot-stuffing: if line starts with "..", remove one dot
		if len(trimmed) > 0 && trimmed[0] == '.' {
			line = line[1:]
		}

		data = append(data, line...)

		// Enforce SIZE limit (10MB as advertised in EHLO)
		if len(data) > 10*1024*1024 {
			slog.Warn("smtp: message exceeds size limit", "remote", s.remoteAddr, "size", len(data))
			s.reply(552, "Message exceeds maximum size")
			return
		}
	}

	s.data = data

	// Parse the message and deliver to each recipient
	subject, bodyText, bodyHTML, messageID, senderName, inReplyTo, references, toList, ccList := parseRawMessage(data)

	for _, rcpt := range s.rcptTo {
		// Check if this is a local recipient
		check, err := s.api.CheckMailbox(rcpt)
		if err != nil || !check.Data.Exists {
			// Non-local: insert into outbound queue for the queue worker to deliver
			recipientDomain := rcpt
			if idx := strings.LastIndex(rcpt, "@"); idx >= 0 {
				recipientDomain = rcpt[idx+1:]
			}
			queueEntry := models.OutboundQueue{
				Sender:     s.mailFrom,
				Recipient:  rcpt,
				Domain:     recipientDomain,
				RawMessage: string(data),
				Status:     "pending",
				MaxRetries: 30,
				ExpiresAt:  time.Now().Add(72 * time.Hour),
			}
			if err := s.db.Create(&queueEntry).Error; err != nil {
				slog.Error("smtp: failed to queue message", "from", s.mailFrom, "to", rcpt, "error", err)
				s.reply(451, "Temporary delivery failure")
				return
			}
			slog.Info("smtp: queued for outbound delivery", "from", s.mailFrom, "to", rcpt, "queue_id", queueEntry.ID)
			continue
		}

		// Local delivery via API
		deliverReq := &apiclient.DeliverRequest{
			Address:    rcpt,
			Sender:     s.mailFrom,
			SenderName: senderName,
			Subject:    subject,
			BodyText:   bodyText,
			BodyHTML:   bodyHTML,
			MessageID:  messageID,
			InReplyTo:  inReplyTo,
			References: references,
			RawMessage: string(data),
			ClientIP:   extractIP(s.remoteAddr),
			HeloName:   s.heloName,
		}
		if len(toList) > 0 {
			toJSON, _ := json.Marshal(toList)
			deliverReq.RecipientsTo = toJSON
		}
		if len(ccList) > 0 {
			ccJSON, _ := json.Marshal(ccList)
			deliverReq.RecipientsCc = ccJSON
		}

		_, err = s.api.DeliverMessage(deliverReq)
		if err != nil {
			slog.Error("smtp: delivery failed", "from", s.mailFrom, "to", rcpt, "error", err)
			// Map API error codes to SMTP reply codes
			if apiErr, ok := err.(*apiclient.APIError); ok {
				switch {
				case apiErr.StatusCode == 403 || apiErr.StatusCode == 550:
					s.reply(550, "Rejected by policy")
				case apiErr.StatusCode == 503 || apiErr.StatusCode == 451:
					s.reply(451, "Try again later")
				default:
					s.reply(451, "Temporary delivery failure")
				}
			} else {
				s.reply(451, "Temporary delivery failure")
			}
			return
		}

		slog.Info("smtp: message delivered", "from", s.mailFrom, "to", rcpt, "subject", subject)
	}

	s.reply(250, "OK: message accepted for delivery")

	// Reset session for next message
	s.mailFrom = ""
	s.rcptTo = nil
	s.data = nil
}

func (s *Session) handleRSET() {
	s.mailFrom = ""
	s.rcptTo = nil
	s.data = nil
	s.reply(250, "OK")
}

func (s *Session) reply(code int, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(s.writer, "%d %s\r\n", code, msg)
	s.writer.Flush()
}
