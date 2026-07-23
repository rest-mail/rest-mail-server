package smtp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/restmail/restmail/internal/gateway/apiclient"
	"github.com/restmail/restmail/internal/gateway/connlimiter"
	rmail "github.com/restmail/restmail/internal/mail"
)

// defaultMaxMessageSize is the maximum accepted message size, in bytes, when
// the deployment does not configure one (SMTP_MAX_MESSAGE_SIZE). The value is
// both advertised in EHLO (SIZE) and enforced during MAIL (SIZE= parameter)
// and DATA — go-smtp derives all three from Server.MaxMessageBytes, so they
// cannot drift.
const defaultMaxMessageSize int64 = 10 * 1024 * 1024 // 10 MiB

// maxRecipients caps the number of RCPT TO commands per transaction. Enforced
// by go-smtp (452 reply beyond the limit).
const maxRecipients = 100

// session implements go-smtp's Session and AuthSession, delegating every
// policy decision to the package-local Backend and Store interfaces. All
// protocol mechanics (command parsing, STARTTLS, SASL exchange, DATA
// dot-reader with SMTP-smuggling hardening) live in go-smtp.
type session struct {
	conn         *gosmtp.Conn
	api          Backend
	store        Store
	limiter      *connlimiter.Limiter
	isSubmission bool // port 587/465 requires AUTH

	// Authenticated user state (submission only).
	authenticated bool
	authEmail     string
	accountID     uint

	// Current transaction.
	mailFrom string
	rcptTo   []string
}

var _ gosmtp.AuthSession = (*session)(nil)

// remoteAddr returns the peer address, as rewritten by the PROXY protocol
// listener when it is in play.
func (s *session) remoteAddr() string {
	return s.conn.Conn().RemoteAddr().String()
}

// AuthMechanisms advertises AUTH only on submission ports. go-smtp
// additionally withholds AUTH until after STARTTLS when a TLS config is set
// (AllowInsecureAuth is enabled only for TLS-less deployments).
func (s *session) AuthMechanisms() []string {
	if !s.isSubmission {
		return nil
	}
	return []string{sasl.Plain, sasl.Login}
}

// Auth returns a SASL server for the requested mechanism. Both mechanisms
// authenticate against the REST API via Backend.Login.
func (s *session) Auth(mech string) (sasl.Server, error) {
	if !s.isSubmission {
		return nil, &gosmtp.SMTPError{
			Code:         503,
			EnhancedCode: gosmtp.EnhancedCode{5, 5, 1},
			Message:      "AUTH not available on this port",
		}
	}
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(identity, username, password string) error {
			if identity != "" && identity != username {
				return &gosmtp.SMTPError{
					Code:         535,
					EnhancedCode: gosmtp.EnhancedCode{5, 7, 8},
					Message:      "Authentication failed",
				}
			}
			return s.login(username, password)
		}), nil
	case sasl.Login:
		return &loginSASLServer{authenticate: s.login}, nil
	default:
		return nil, gosmtp.ErrAuthUnknownMechanism
	}
}

// loginSASLServer is a server for the obsolete LOGIN mechanism, which go-sasl
// only ships a client for. Legacy mail clients still use it; the exchange is
// a base64 "Username:" prompt, then "Password:".
type loginSASLServer struct {
	authenticate func(username, password string) error
	username     string
	started      bool
	gotUsername  bool
}

// Next implements sasl.Server. A client-provided initial response is accepted
// as the username, per the LOGIN draft.
func (l *loginSASLServer) Next(response []byte) (challenge []byte, done bool, err error) {
	if !l.started {
		l.started = true
		if response == nil {
			return []byte("Username:"), false, nil
		}
	}
	if !l.gotUsername {
		l.username = string(response)
		l.gotUsername = true
		return []byte("Password:"), false, nil
	}
	return nil, true, l.authenticate(l.username, string(response))
}

// login authenticates against the REST API and tracks failures in the
// connection limiter so repeated bad credentials lead to a ban.
func (s *session) login(username, password string) error {
	ip := extractIP(s.remoteAddr())

	resp, err := s.api.Login(username, password)
	if err != nil {
		slog.Warn("smtp: auth failed",
			"remote", s.remoteAddr(),
			"user", username,
			"event", "smtp_auth_failed",
			"ip", ip,
		)
		s.limiter.RecordAuthFail(ip)
		if s.limiter.IsBanned(ip) {
			return &gosmtp.SMTPError{
				Code:         421,
				EnhancedCode: gosmtp.EnhancedCode{4, 7, 0},
				Message:      "Too many authentication failures",
			}
		}
		return &gosmtp.SMTPError{
			Code:         535,
			EnhancedCode: gosmtp.EnhancedCode{5, 7, 8},
			Message:      "Authentication failed",
		}
	}

	s.limiter.ResetAuth(ip)
	s.authenticated = true
	s.authEmail = username
	s.accountID = resp.Data.User.ID

	slog.Info("smtp: authenticated", "remote", s.remoteAddr(), "user", username)
	return nil
}

// Mail starts a transaction. On submission ports it requires authentication
// and verifies the sender is either the authenticated user or one of its
// linked accounts.
func (s *session) Mail(from string, _ *gosmtp.MailOptions) error {
	if s.isSubmission && !s.authenticated {
		return &gosmtp.SMTPError{
			Code:         530,
			EnhancedCode: gosmtp.EnhancedCode{5, 7, 0},
			Message:      "Authentication required",
		}
	}

	if s.isSubmission && s.authenticated && from != s.authEmail {
		// Check linked accounts. A lookup error is treated as "not authorized",
		// matching the prior behavior (which left the count at zero).
		authorized, err := s.store.SenderAuthorized(s.accountID, from)
		if err != nil || !authorized {
			slog.Warn("smtp: sender not authorized", "auth_user", s.authEmail, "mail_from", from, "error", err)
			return &gosmtp.SMTPError{
				Code:         553,
				EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
				Message:      "Sender address not authorized for this account",
			}
		}
	}

	s.mailFrom = from
	s.rcptTo = nil
	return nil
}

// Rcpt accepts a recipient. Local recipients (per Backend.CheckMailbox) are
// always accepted; unknown recipients are accepted for outbound queueing on
// authenticated submission and rejected 550 on inbound.
func (s *session) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	resp, err := s.api.CheckMailbox(to)
	if err != nil {
		// If the API is unreachable, temp fail.
		slog.Error("smtp: API error checking mailbox", "address", to, "error", err)
		return &gosmtp.SMTPError{
			Code:         451,
			EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
			Message:      "Temporary service failure",
		}
	}

	if !resp.Data.Exists && !(s.isSubmission && s.authenticated) {
		// Not a local recipient — on inbound we reject; on authenticated
		// submission we accept and queue for outbound delivery.
		return &gosmtp.SMTPError{
			Code:         550,
			EnhancedCode: gosmtp.EnhancedCode{5, 1, 1},
			Message:      fmt.Sprintf("No such user - %s", to),
		}
	}

	s.rcptTo = append(s.rcptTo, to)
	return nil
}

// Data receives the message and handles every recipient of the transaction:
// local recipients are delivered via the API, remote ones are enqueued for
// the outbound queue worker.
//
// DATA gets a SINGLE reply for the whole message. Handle every recipient and
// aggregate: only fail the transaction (4xx/5xx) if NOTHING was committed —
// returning an error after some recipients were already delivered/queued
// makes the client retry the entire message, duplicating those recipients.
func (s *session) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		var smtpErr *gosmtp.SMTPError
		if errors.As(err, &smtpErr) {
			// e.g. message exceeds MaxMessageBytes (552).
			slog.Warn("smtp: rejecting DATA", "remote", s.remoteAddr(), "error", err)
			return smtpErr
		}
		slog.Error("smtp: error reading DATA", "remote", s.remoteAddr(), "error", err)
		return &gosmtp.SMTPError{
			Code:         451,
			EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
			Message:      "Error reading message data",
		}
	}

	// Parse the message and deliver to each recipient.
	subject, bodyText, bodyHTML, messageID, senderName, inReplyTo, references, toList, ccList := parseRawMessage(data)

	if messageID == "" {
		messageID = rmail.GenerateMessageID(rmail.DomainFromAddress(s.mailFrom))
	}

	accepted := 0
	failed := 0
	failCode, failEnhanced, failMsg := 451, gosmtp.EnhancedCode{4, 3, 0}, "Temporary delivery failure"
	for _, rcpt := range s.rcptTo {
		// Check if this is a local recipient.
		check, err := s.api.CheckMailbox(rcpt)
		if err != nil || !check.Data.Exists {
			// Non-local: insert into the outbound queue for the queue worker.
			recipientDomain := rcpt
			if idx := strings.LastIndex(rcpt, "@"); idx >= 0 {
				recipientDomain = rcpt[idx+1:]
			}
			if err := s.store.EnqueueOutbound(OutboundMessage{
				Sender:     s.mailFrom,
				Recipient:  rcpt,
				Domain:     recipientDomain,
				RawMessage: string(data),
			}); err != nil {
				slog.Error("smtp: failed to queue message", "from", s.mailFrom, "to", rcpt, "error", err)
				failed++
				continue
			}
			slog.Info("smtp: queued for outbound delivery", "from", s.mailFrom, "to", rcpt)
			accepted++
			continue
		}

		// Local delivery via API.
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
			ClientIP:   extractIP(s.remoteAddr()),
			HeloName:   s.conn.Hostname(),
		}
		if len(toList) > 0 {
			toJSON, _ := json.Marshal(toList)
			deliverReq.RecipientsTo = toJSON
		}
		if len(ccList) > 0 {
			ccJSON, _ := json.Marshal(ccList)
			deliverReq.RecipientsCc = ccJSON
		}

		if _, err := s.api.DeliverMessage(deliverReq); err != nil {
			slog.Error("smtp: delivery failed", "from", s.mailFrom, "to", rcpt, "error", err)
			// Remember a representative reply for the all-failed case.
			var apiErr *apiclient.APIError
			if errors.As(err, &apiErr) {
				switch {
				case apiErr.StatusCode == 403 || apiErr.StatusCode == 550:
					failCode, failEnhanced, failMsg = 550, gosmtp.EnhancedCode{5, 7, 1}, "Rejected by policy"
				case apiErr.StatusCode == 503 || apiErr.StatusCode == 451:
					failCode, failEnhanced, failMsg = 451, gosmtp.EnhancedCode{4, 3, 0}, "Try again later"
				default:
					failCode, failEnhanced, failMsg = 451, gosmtp.EnhancedCode{4, 3, 0}, "Temporary delivery failure"
				}
			} else {
				failCode, failEnhanced, failMsg = 451, gosmtp.EnhancedCode{4, 3, 0}, "Temporary delivery failure"
			}
			failed++
			continue
		}

		slog.Info("smtp: message delivered", "from", s.mailFrom, "to", rcpt, "subject", subject)
		accepted++
	}

	// Single transaction reply: fail only if NOTHING was committed.
	if accepted == 0 {
		return &gosmtp.SMTPError{Code: failCode, EnhancedCode: failEnhanced, Message: failMsg}
	}
	if failed > 0 {
		slog.Warn("smtp: partial delivery accepted to avoid duplicate retry",
			"from", s.mailFrom, "accepted", accepted, "failed", failed)
	}
	return nil
}

// Reset discards the current transaction (RSET, or implicitly after DATA).
func (s *session) Reset() {
	s.mailFrom = ""
	s.rcptTo = nil
}

// Logout is called when the connection is closed.
func (s *session) Logout() error {
	return nil
}
