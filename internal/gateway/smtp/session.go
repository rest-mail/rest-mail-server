package smtp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/rest-mail/go-smtp"

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
	subLimiter   *submissionRateLimiter // per-account submission cap (nil = disabled)
	hostname     string                 // this server's host name, stamped into the Received header
	isSubmission bool                   // port 587/465 requires AUTH

	// Anti-abuse tarpit: the connection-scoped context is cancelled on server
	// shutdown so an in-flight tarpit sleep aborts rather than blocking the
	// shutdown or outliving the connection. sleep is the (injectable) delay
	// primitive — tarpitSleep in production, a fake in tests.
	ctx     context.Context
	tarpit  tarpitPolicy
	sleep   func(context.Context, time.Duration)
	tarpitN int // cumulative rejections/auth-failures on this connection

	// Authenticated user state (submission only).
	authenticated bool
	authEmail     string
	accountID     uint

	// Current transaction.
	mailFrom string
	rcptTo   []string
}

// tarpitReject records one connection-level rejection (an invalid inbound RCPT
// or an AUTH failure) and, once the count crosses the soft limit, sleeps the
// escalating (capped) delay before the caller hands the rejection back to the
// client. Legitimate senders (0-1 errors) never sleep. The sleep aborts on
// context cancellation, so a closed connection or a server shutdown never
// leaves the goroutine hanging.
func (s *session) tarpitReject() {
	s.tarpitN++
	d := s.tarpit.delayFor(s.tarpitN)
	if d <= 0 {
		return
	}
	slog.Warn("smtp: tarpitting abusive session",
		"remote", s.remoteAddr(),
		"errors", s.tarpitN,
		"delay", d.String(),
	)
	s.sleep(s.ctx, d)
}

var _ gosmtp.AuthSession = (*session)(nil)

// remoteAddr returns the peer address, as rewritten by the PROXY protocol
// listener when it is in play.
func (s *session) remoteAddr() string {
	return s.conn.Conn().RemoteAddr().String()
}

// transferConn digs the anti-slowloris tracker out of the session's network
// connection so Data can arm it around the message-body transfer. go-smtp
// hands back either the accepted conn directly or, when TLS is active
// (implicit on 465, or after STARTTLS replaced the conn), the *tls.Conn
// wrapped around it — NetConn unwraps that one layer to reach the tracker the
// accept path installed. If the unwrap still doesn't find the tracker
// (unexpected wrapping), fail OPEN: returning nil skips enforcement — never
// break mail because introspection failed.
func (s *session) transferConn() *transferRateConn {
	conn := s.conn.Conn()
	if tc, ok := conn.(*tls.Conn); ok {
		conn = tc.NetConn()
	}
	rc, ok := conn.(*transferRateConn)
	if !ok {
		slog.Debug("smtp: transfer-rate enforcement unavailable: unexpected connection wrapping",
			"remote", s.remoteAddr(), "conn_type", fmt.Sprintf("%T", conn))
		return nil
	}
	return rc
}

// tlsVersionName maps a crypto/tls version constant to a stable, human-readable
// label for the transport-security metrics. Unknown versions yield "" so the
// stored value never encodes an internal constant.
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLS1.3"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS10:
		return "TLS1.0"
	default:
		return ""
	}
}

// inboundTransportSecurity derives the inbound transport-security fields for a
// DeliverRequest from the connection's TLS state. This monitoring is scoped to
// inbound-MX (port 25) mail — the plaintext-capable path from the public
// internet — so on the authenticated submission path (587/465) it returns
// (nil, "", ""): submission is authenticated client traffic tracked separately,
// and a nil ReceivedTLS is persisted as NULL ("not applicable").
//
// On the inbound-MX path it ALWAYS returns a non-nil encrypted flag so the
// message joins the inbound-MX denominator, regardless of whether the peer used
// TLS — the whole point is to count plaintext arrivals. TLS version/cipher are
// best-effort: populated when encrypted, empty otherwise.
func inboundTransportSecurity(isSubmission bool, state tls.ConnectionState, isTLS bool) (received *bool, version, cipher string) {
	if isSubmission {
		return nil, "", ""
	}
	encrypted := isTLS
	received = &encrypted
	if isTLS {
		version = tlsVersionName(state.Version)
		cipher = tls.CipherSuiteName(state.CipherSuite)
	}
	return received, version, cipher
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

// tooManyAuthFailures is the 421 reply sent when a client or the targeted
// account is banned. 421 closes the connection, so a banned attacker cannot keep
// pipelining AUTH commands down the same session.
var tooManyAuthFailures = &gosmtp.SMTPError{
	Code:         421,
	EnhancedCode: gosmtp.EnhancedCode{4, 7, 0},
	Message:      "Too many authentication failures",
}

// login authenticates against the REST API and tracks failures in the
// connection limiter so repeated bad credentials lead to a ban.
//
// Brute-force protection is layered: failures are throttled BOTH per source IP
// and per targeted account, so neither a single host hammering many accounts nor
// a rotating botnet hammering one account slips through. A ban is a HARD stop —
// once the IP or the account is banned the request is rejected outright, WITHOUT
// calling the auth API. That keeps a banned attacker off the backend and means a
// ban is never lifted by a lucky correct guess, because no guess is verified
// while banned.
func (s *session) login(username, password string) error {
	ip := extractIP(s.remoteAddr())

	// Hard stop before any API call: reject a banned client or a banned target
	// account without verifying the password.
	if s.limiter.IsBanned(ip) || s.limiter.IsUserBanned(username) {
		slog.Warn("smtp: auth rejected: banned",
			"remote", s.remoteAddr(),
			"user", maskEmail(username),
			"event", "smtp_auth_banned",
			"ip", ip,
		)
		s.tarpitReject()
		return tooManyAuthFailures
	}

	resp, err := s.api.Login(username, password)
	if err != nil {
		// OSI-5: the attempted username is attacker-controlled and high-volume on
		// this path (credential-stuffing / user-enumeration probes), so it is
		// masked rather than logged in the clear. The authenticated (success) path
		// below still logs the real user for audit.
		slog.Warn("smtp: auth failed",
			"remote", s.remoteAddr(),
			"user", maskEmail(username),
			"event", "smtp_auth_failed",
			"ip", ip,
		)

		// Only a DEFINITIVE credential rejection (401/403) is a brute-force signal.
		// A transient API/network error leaves the password unverified, so it must
		// not accrue against the ban — otherwise a brief API outage would ban
		// legitimate clients' IPs and accounts. Surface it as a temporary failure.
		if !apiclient.IsAuthRejection(err) {
			return &gosmtp.SMTPError{
				Code:         454,
				EnhancedCode: gosmtp.EnhancedCode{4, 7, 0},
				Message:      "Temporary authentication failure, try again later",
			}
		}

		// Count the failure against both the source IP and the target account, then
		// feed the tarpit so repeated bad credentials on one connection are
		// progressively slowed.
		s.limiter.RecordAuthFail(ip)
		s.limiter.RecordAuthFailUser(username)
		s.tarpitReject()
		if s.limiter.IsBanned(ip) || s.limiter.IsUserBanned(username) {
			return tooManyAuthFailures
		}
		return &gosmtp.SMTPError{
			Code:         535,
			EnhancedCode: gosmtp.EnhancedCode{5, 7, 8},
			Message:      "Authentication failed",
		}
	}

	s.limiter.ResetAuth(ip)
	s.limiter.ResetAuthUser(username)
	s.authenticated = true
	s.authEmail = username
	s.accountID = resp.Data.User.ID

	slog.Info("smtp: authenticated", "remote", s.remoteAddr(), "user", username)
	return nil
}

// rateLimitKey identifies the account a submission is charged against for the
// per-account rate limit. Linked accounts share one webmail account id, so a
// compromised credential is capped as a single principal regardless of which of
// its authorized From addresses it sends as. It falls back to the authenticated
// login when no account id is set.
func (s *session) rateLimitKey() string {
	if s.accountID != 0 {
		return fmt.Sprintf("acct:%d", s.accountID)
	}
	return "user:" + s.authEmail
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
			// OSI-5: mask the addresses — a mismatching MAIL FROM is a spoofing
			// attempt and both values are logged in the clear otherwise.
			slog.Warn("smtp: sender not authorized", "auth_user", maskEmail(s.authEmail), "mail_from", maskEmail(from), "error", err)
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

// authorizeFromHeader enforces that an authenticated submitter's message From:
// header address is one the account is authorized to send as: its own login
// identity, or a linked address (the same linked_accounts -> mailboxes lookup as
// the MAIL FROM check). A From header for an address the account does not own —
// or a missing/unparseable From — is rejected 550, preventing header-level
// spoofing (#181). Like Mail(), a store lookup error is treated as "not
// authorized"; both the authenticated user and the offending From are masked in
// logs since a mismatch is a spoofing signal.
func (s *session) authorizeFromHeader(fromAddr string) error {
	if fromAddr == "" {
		slog.Warn("smtp: submission rejected: no usable From header", "auth_user", maskEmail(s.authEmail))
		return &gosmtp.SMTPError{
			Code:         550,
			EnhancedCode: gosmtp.EnhancedCode{5, 6, 0},
			Message:      "A valid From header is required",
		}
	}
	if fromAddr == s.authEmail {
		return nil
	}
	authorized, err := s.store.SenderAuthorized(s.accountID, fromAddr)
	if err != nil || !authorized {
		slog.Warn("smtp: From header not authorized",
			"auth_user", maskEmail(s.authEmail), "header_from", maskEmail(fromAddr), "error", err)
		return &gosmtp.SMTPError{
			Code:         550,
			EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
			Message:      "From header address not authorized for this account",
		}
	}
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
		// submission we accept and queue for outbound delivery. A rejected
		// inbound RCPT is the recipient-enumeration/dictionary signal, so it
		// feeds the tarpit: past the soft limit each further miss is delayed.
		s.tarpitReject()
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
	// Message-body transfer begins here (go-smtp calls Data for both DATA and
	// BDAT): arm the anti-slowloris tracker for the duration of the body read
	// and disarm the moment it ends, so between-command idling is never
	// subject to the rate policy.
	if tc := s.transferConn(); tc != nil {
		tc.arm()
		defer tc.disarm()
	}

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

	// Per-account submission rate limit (#171): a compromised credential must not
	// be able to flood outbound mail. Enforced after the body is fully read (so the
	// DATA stream is consumed) and before any recipient is delivered or queued, so a
	// throttled message commits nothing. Only authenticated submission is limited;
	// inbound-MX (port 25) has no account to key on.
	if s.isSubmission && s.authenticated && !s.subLimiter.Allow(s.rateLimitKey()) {
		slog.Warn("smtp: submission rate limit exceeded",
			"user", s.authEmail, "account_id", s.accountID, "event", "smtp_submission_rate_limited")
		return &gosmtp.SMTPError{
			Code:         451,
			EnhancedCode: gosmtp.EnhancedCode{4, 7, 1},
			Message:      "Account rate limit exceeded, try again later",
		}
	}

	// Parse the message and deliver to each recipient.
	subject, bodyText, bodyHTML, messageID, senderName, inReplyTo, references, toList, ccList, fromAddr := parseRawMessage(data)

	// #181: on the authenticated submission path the message From: header must
	// belong to an identity the account may send as — mirroring the MAIL FROM
	// check in Mail(). The envelope check alone does not catch an authenticated
	// user forging From: "CEO" <ceo@example.com>, which recipients' clients
	// display and which would otherwise be stored/relayed verbatim. Enforced
	// here, before anything is delivered, queued, or persisted, so a rejected
	// spoof commits nothing.
	if s.isSubmission && s.authenticated {
		if err := s.authorizeFromHeader(fromAddr); err != nil {
			return err
		}
	}

	if messageID == "" {
		messageID = rmail.GenerateMessageID(rmail.DomainFromAddress(s.mailFrom))
	}

	// Capture inbound transport-security at DATA time: was this connection
	// encrypted (implicit TLS on 465, or STARTTLS-upgraded), and with what
	// version/cipher. go-smtp reports the live TLS state off the underlying
	// conn (a *tls.Conn when TLS is active). Only the inbound-MX path records
	// it; on submission these stay nil/empty (see inboundTransportSecurity).
	tlsState, isTLS := s.conn.TLSConnectionState()
	receivedTLS, tlsVersion, tlsCipher := inboundTransportSecurity(s.isSubmission, tlsState, isTLS)

	// Prepend our trace (Received) header (RFC 5321 §4.4): an SMTP server that
	// accepts a message for delivery or further processing MUST insert trace
	// information at the top of the content, so this hop is visible in the
	// Received chain (loop detection, DMARC/ARC alignment) and the server behaves
	// like a mainstream MTA. Stamped once onto the raw bytes here so it flows to
	// every downstream use — local delivery, the outbound queue, and the persisted
	// submission reference — identically. It sits above any DKIM-Signature, which
	// is standard and does not invalidate existing signatures. The "for" clause is
	// emitted only for a single recipient (see singleRecipient) so a Bcc recipient
	// is never disclosed to the others.
	received := rmail.BuildReceivedHeader(rmail.ReceivedInfo{
		From:      s.conn.Hostname(),
		RemoteIP:  extractIP(s.remoteAddr()),
		By:        s.hostname,
		With:      receivedWith(isTLS, s.authenticated),
		ID:        rmail.GenerateQueueID(),
		For:       singleRecipient(s.rcptTo),
		Timestamp: time.Now(),
	})
	data = append([]byte(received), data...)

	accepted := 0
	failed := 0
	failCode, failEnhanced, failMsg := 451, gosmtp.EnhancedCode{4, 3, 0}, "Temporary delivery failure"

	// DSN provenance: the first time this submission produces outbound mail we
	// persist a sender-owned reference for it and link every outbound queue row
	// to that reference, so a later bounce/DSN can be authenticated back to the
	// submitting mailbox. submittedMsgID stays nil when nothing was persisted.
	var submittedMsgID *uint
	submittedOnce := false

	for _, rcpt := range s.rcptTo {
		// Check if this is a local recipient.
		check, err := s.api.CheckMailbox(rcpt)
		if err != nil || !check.Data.Exists {
			// Non-local: insert into the outbound queue for the queue worker.
			recipientDomain := rcpt
			if idx := strings.LastIndex(rcpt, "@"); idx >= 0 {
				recipientDomain = rcpt[idx+1:]
			}

			// Persist the submission reference once per message (authenticated
			// submissions only). A failure must not fail delivery — proceed with
			// no linked reference.
			if s.authenticated && !submittedOnce {
				submittedOnce = true
				var toJSON, ccJSON []byte
				if len(toList) > 0 {
					toJSON, _ = json.Marshal(toList)
				}
				if len(ccList) > 0 {
					ccJSON, _ = json.Marshal(ccList)
				}
				ref, perr := s.store.PersistSubmittedMessage(SubmittedMessage{
					Sender:       s.mailFrom,
					MessageID:    messageID,
					SenderName:   senderName,
					Subject:      subject,
					BodyText:     bodyText,
					BodyHTML:     bodyHTML,
					InReplyTo:    inReplyTo,
					References:   references,
					RawMessage:   string(data),
					RecipientsTo: toJSON,
					RecipientsCc: ccJSON,
				})
				if perr != nil {
					slog.Warn("smtp: failed to persist submission reference for DSN provenance",
						"from", s.mailFrom, "error", perr)
				} else {
					submittedMsgID = ref
				}
			}

			if err := s.store.EnqueueOutbound(OutboundMessage{
				Sender:     s.mailFrom,
				Recipient:  rcpt,
				Domain:     recipientDomain,
				RawMessage: string(data),
				MessageID:  submittedMsgID,
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
			RawMessage: data,
			ClientIP:   extractIP(s.remoteAddr()),
			HeloName:   s.conn.Hostname(),
			// Always-on inbound transport-security metrics (inbound-MX only).
			ReceivedTLS: receivedTLS,
			TLSVersion:  tlsVersion,
			TLSCipher:   tlsCipher,
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
