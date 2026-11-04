package smtp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-sasl"
	gosmtp "github.com/rest-mail/go-smtp/pkg/smtp"
	smtpserver "github.com/rest-mail/go-smtp/pkg/smtp/server"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/ratelimit"
	"github.com/rest-mail/rest-mail-server/internal/srs"
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
	conn         *smtpserver.Conn
	api          Backend
	store        Store
	limiter      *connlimiter.Limiter
	subLimiter   *ratelimit.SubmissionLimiter // per-account submission cap (nil = disabled)
	hostname     string                       // this server's host name, stamped into the Received header
	isSubmission bool                         // port 465 requires AUTH

	// srs checks SRS addresses (nil: no secret configured). srsBounceMax and
	// srsWindow are the per-recipient DSN limit a bounce to one is held to.
	srs          *srs.Signer
	srsBounceMax int
	srsWindow    time.Duration

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
	// credentialID and credentialKind are the credential the session signed
	// in with: one of the account's passwords, or an app password. It is
	// checked again before each MAIL FROM. An app password's scope is its own
	// mailbox: it sends as that address only, never as another account's.
	credentialID   uint
	credentialKind string

	// Current transaction.
	mailFrom string
	rcptTo   []string
	// srsRelays maps a recipient accepted as a valid SRS address to the sender it
	// carries, who is told of the bounce. Reset with rcptTo.
	srsRelays map[string]string
	// bodyType is the SMTP BODY= parameter the client declared on MAIL FROM
	// (7BIT/8BITMIME/BINARYMIME, or empty). It is threaded onto each outbound
	// queue row so the delivery worker never relays 8-bit content to a next hop
	// that does not advertise 8BITMIME (RFC 6152). Reset on every MAIL.
	bodyType string
	// smtpUTF8 applies only to this MAIL transaction, not the connection.
	smtpUTF8 bool
	// requireTLS is this transaction's REQUIRETLS: its sender requires TLS on every hop
	// (RFC 8689 §4.1). go-smtp accepts it only on a TLS session.
	requireTLS bool
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

var _ smtpserver.AuthSession = (*session)(nil)

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
// internet — so on the authenticated submission path (465) it returns
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
// authenticate against the REST API via Backend.SignIn.
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
		return nil, smtpserver.ErrAuthUnknownMechanism
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

	resp, err := s.api.SignIn(username, password, "smtp")
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
	s.credentialID = resp.Data.CredentialID
	s.credentialKind = resp.Data.CredentialKind

	slog.Info("smtp: authenticated", "remote", s.remoteAddr(), "user", username)
	return nil
}

// rateLimitKey identifies the account a submission is charged against for the
// per-account rate limit: the account signed in, so a compromised credential
// is capped as a single principal regardless of which of its authorized From
// addresses it sends as. It falls back to the authenticated login when no
// account id is set.
func (s *session) rateLimitKey() string {
	if s.accountID != 0 {
		return fmt.Sprintf("acct:%d", s.accountID)
	}
	return "user:" + s.authEmail
}

// Mail starts a transaction. On submission ports it requires authentication
// and verifies the sender is either the account signed in or one it may send
// as (a mail:send grant on it).
func (s *session) Mail(from string, opts *gosmtp.MailOptions) error {
	// Port 25 takes mail in the clear. RFC 3207 §4: "A publicly-referenced SMTP server
	// MUST NOT require use of the STARTTLS extension in order to deliver mail locally."
	// Refusing until STARTTLS had been used made rest-mail unreachable for any sender
	// that will not upgrade, which is a delivery failure rather than a security win
	// (issue #292). STARTTLS is still advertised on every cleartext session, the arrival
	// is recorded as plaintext (see inboundTransportSecurity), and penalising plaintext
	// is the inbound pipeline's job.
	//
	// Submission is different: the client is ours and authenticates, so it can be
	// required to use TLS, and is. The gate is on the transaction rather than the
	// connection deliberately: EHLO, NOOP, RSET and STARTTLS itself must work, or a
	// client has no way to upgrade.
	if _, isTLS := s.conn.TLSConnectionState(); !isTLS && s.isSubmission {
		return &gosmtp.SMTPError{
			Code:         530,
			EnhancedCode: gosmtp.EnhancedCode{5, 7, 0},
			Message:      "Must issue a STARTTLS command first",
		}
	}

	if s.isSubmission && !s.authenticated {
		return &gosmtp.SMTPError{
			Code:         530,
			EnhancedCode: gosmtp.EnhancedCode{5, 7, 0},
			Message:      "Authentication required",
		}
	}
	if err := validateUTF8Address(from, opts != nil && opts.UTF8, 550); err != nil {
		return err
	}

	// A session signed in with an app password holds it only while it lasts:
	// revoked, or expired, it sends nothing more.
	if s.isSubmission && s.authenticated && s.credentialID != 0 {
		usable, err := s.store.CredentialUsable(s.credentialID)
		if err != nil {
			slog.Error("smtp: could not check the credential", "user", maskEmail(s.authEmail), "error", err)
			return &gosmtp.SMTPError{
				Code:         451,
				EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
				Message:      "Temporary service failure",
			}
		}
		if !usable {
			slog.Warn("smtp: credential no longer usable", "user", maskEmail(s.authEmail), "event", "smtp_credential_unusable")
			s.authenticated, s.authEmail, s.accountID, s.credentialID, s.credentialKind = false, "", 0, 0, ""
			return &gosmtp.SMTPError{
				Code:         530,
				EnhancedCode: gosmtp.EnhancedCode{5, 7, 0},
				Message:      "Authentication required",
			}
		}
	}

	if s.isSubmission && s.authenticated {
		// Resolve the account identity before considering grants. A lookup
		// error is treated as "not authorized".
		authorized, err := s.maySendAs(from)
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
	s.srsRelays = nil
	// Remember the declared BODY= type so the outbound path can honor RFC 6152
	// (never relay 8-bit content to a 7-bit-only next hop). Reset per transaction.
	s.bodyType = ""
	s.smtpUTF8 = opts != nil && opts.UTF8
	s.requireTLS = opts != nil && opts.RequireTLS
	if opts != nil {
		s.bodyType = string(opts.Body)
	}
	return nil
}

// authorizeFromHeader enforces that an authenticated submitter's message From:
// header address is one the account is authorized to send as: its own login
// identity, or an account it may send as (the same grants check as the MAIL
// FROM check). A From header for an address the account does not own —
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
	authorized, err := s.maySendAs(fromAddr)
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

// followSendingPolicy holds an authenticated submission to the sending policy
// of its From domain, as the send API holds a message it composes (#144): mail
// from a bulk domain must carry a List-Unsubscribe header, and mail from a
// transactional domain must not. A policy that cannot be read is a temporary
// refusal, so the client retries rather than the rule being skipped.
func (s *session) followSendingPolicy(fromAddr string, message io.Reader) error {
	policy, err := s.store.SendingPolicy(fromAddr)
	if err != nil {
		slog.Error("smtp: could not read the sending policy", "header_from", maskEmail(fromAddr), "error", err)
		return &gosmtp.SMTPError{
			Code:         451,
			EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
			Message:      "Could not check the domain's sending policy, try again later",
		}
	}
	unsubscribe := false
	// Reads the header block and no further. Only a message whose headers the MIME parser read
	// reaches here, so the block is within the parser's own limit on it.
	if msg, perr := mail.ReadMessage(message); perr == nil {
		unsubscribe = strings.TrimSpace(msg.Header.Get("List-Unsubscribe")) != ""
	}
	switch {
	case policy == models.SendingPolicyBulk && !unsubscribe:
		return &gosmtp.SMTPError{
			Code:         550,
			EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
			Message:      "This domain sends bulk mail: a List-Unsubscribe header is required",
		}
	case policy == models.SendingPolicyTransactional && unsubscribe:
		return &gosmtp.SMTPError{
			Code:         550,
			EnhancedCode: gosmtp.EnhancedCode{5, 7, 1},
			Message:      "This domain sends transactional mail: a List-Unsubscribe header is not allowed",
		}
	}
	return nil
}

// maySendAs accepts the login spelling or another spelling that resolves to
// the same mailbox ID. Only a password session may use grants to send as a
// different mailbox; app passwords remain scoped to their own identity.
// Sending as its own mailbox is checked against the account's grants too, on
// every call, so the server's capability ceiling stops a session signed in
// before mail:send was removed.
func (s *session) maySendAs(addr string) (bool, error) {
	same := addr == s.authEmail
	if !same {
		var err error
		if same, err = s.store.SenderMatchesAccount(s.accountID, addr); err != nil {
			return false, err
		}
	}
	if same {
		return s.store.AccountMaySend(s.accountID)
	}
	if s.credentialKind != "password" {
		return false, nil
	}
	return s.store.SenderAuthorized(s.accountID, addr)
}

// Rcpt accepts a recipient. Local recipients (per Backend.CheckMailbox) are
// always accepted; unknown recipients are accepted for outbound queueing on
// authenticated submission and rejected 550 on inbound.
func (s *session) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	if err := validateUTF8Address(to, s.smtpUTF8, 553); err != nil {
		return err
	}
	if handled, err := s.rcptSRS(to); handled {
		return err
	}
	resp, err := s.api.CheckMailbox(to)
	if err != nil {
		if reason, refused := aliasRoutingRefusal(err); refused {
			s.tarpitReject()
			return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 1, 1}, Message: reason}
		}
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
			Message:      "No such user",
		}
	}

	s.rcptTo = append(s.rcptTo, to)
	return nil
}

// rcptSRS handles an inbound recipient that is an SRS address under a domain this
// server hosts, where a bounce for mail it forwarded comes back (#775). It is
// accepted only when its hash and age check out, and a forged or expired one is
// refused as an unknown recipient is, feeding the tarpit. It reports handled false
// for any other recipient. An authenticated submission is not a bounce coming back:
// its recipient goes out as any other does, and is checked at the MX it reaches.
func (s *session) rcptSRS(to string) (handled bool, err error) {
	if s.srs == nil || (s.isSubmission && s.authenticated) || !srs.IsAddress(to) {
		return false, nil
	}
	forwardingDomain := rmail.DomainFromAddress(to)
	hosted, err := s.store.HostedDomain(forwardingDomain)
	if err != nil {
		slog.Error("smtp: could not check the domain of an SRS address", "domain", forwardingDomain, "error", err)
		return true, &gosmtp.SMTPError{
			Code:         451,
			EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
			Message:      "Temporary service failure",
		}
	}
	if !hosted {
		return false, nil
	}
	original, err := s.srs.Reverse(to, time.Now())
	if err != nil {
		slog.Info("smtp: refusing an SRS address", "remote", s.remoteAddr(), "reason", err)
		s.tarpitReject()
		return true, &gosmtp.SMTPError{
			Code:         550,
			EnhancedCode: gosmtp.EnhancedCode{5, 1, 1},
			Message:      "No such user",
		}
	}
	if s.srsRelays == nil {
		s.srsRelays = make(map[string]string)
	}
	s.srsRelays[to] = original
	s.rcptTo = append(s.rcptTo, to)
	return true, nil
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

	// The message goes to a temporary file as it arrives, and is read from there. The reader stops
	// with the 552 at the size cap, so the file never holds more than the cap. The file goes on
	// every way out of this function.
	message, err := spool(r)
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
	defer message.remove()

	// Validate before parsing, rate accounting, or any delivery/queue write.
	// The MIME body may contain arbitrary 8-bit data without SMTPUTF8.
	if err := validateUTF8Headers(message.reader(), s.smtpUTF8); err != nil {
		return err
	}

	// Hop count (RFC 5321 §6.3): a message carrying more Received: fields than the limit,
	// counting the one this server stamps below, is taken as a mail loop and refused for good,
	// as Postfix's hopcount_limit refuses it (RFC 3463 5.4.6, routing loop detected).
	receivedCount, err := rmime.CountReceived(message.reader())
	if err != nil {
		slog.Error("smtp: error reading the message from disk", "remote", s.remoteAddr(), "error", err)
		return messageDiskFailure()
	}
	if receivedCount+1 > rmime.CurrentLimits().MaxReceived {
		slog.Warn("smtp: rejecting DATA: too many Received headers", "remote", s.remoteAddr(), "event", "smtp_hop_limit")
		return &gosmtp.SMTPError{
			Code:         554,
			EnhancedCode: gosmtp.EnhancedCode{5, 4, 6},
			Message:      "Routing loop detected: too many Received headers",
		}
	}

	// Per-account submission rate limit (#171): a compromised credential must not
	// be able to flood outbound mail. Enforced after the body is fully read (so the
	// DATA stream is consumed) and before any recipient is delivered or queued, so a
	// throttled message commits nothing. Only authenticated submission is limited;
	// inbound-MX (port 25) has no account to key on.
	budget, persistentBudget := s.store.(SubmissionBudget)
	persistentBudget = persistentBudget && budget.PersistentSendLimits()
	if s.isSubmission && s.authenticated && !persistentBudget && !s.subLimiter.Allow(s.rateLimitKey()) {
		slog.Warn("smtp: submission rate limit exceeded",
			"user", s.authEmail, "account_id", s.accountID, "event", "smtp_submission_rate_limited")
		return &gosmtp.SMTPError{
			Code:         451,
			EnhancedCode: gosmtp.EnhancedCode{4, 7, 1},
			Message:      "Account rate limit exceeded, try again later",
		}
	}

	// Parse the message and deliver to each recipient.
	parsed, parseErr := parseSpooled(message)
	if errors.Is(parseErr, rmime.ErrStructureLimit) {
		// Nested deeper or holding more parts than the MIME limits allow (#1641): a media
		// problem the sender must fix, so refused for good (RFC 3463 5.6.0).
		slog.Warn("smtp: rejecting DATA: MIME structure over limit", "remote", s.remoteAddr(), "error", parseErr, "event", "smtp_mime_limit")
		return &gosmtp.SMTPError{
			Code:         554,
			EnhancedCode: gosmtp.EnhancedCode{5, 6, 0},
			Message:      "Message structure too complex: too deeply nested or too many MIME parts",
		}
	}
	if parseErr != nil {
		slog.Error("smtp: error reading the message from disk", "remote", s.remoteAddr(), "error", parseErr)
		return messageDiskFailure()
	}
	defer parsed.remove()
	subject, messageID, senderName, inReplyTo, references, toList, ccList, fromAddr := parsed.subject, parsed.messageID, parsed.senderName, parsed.inReplyTo, parsed.references, parsed.toList, parsed.ccList, parsed.fromAddr

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
		if err := s.followSendingPolicy(fromAddr, message.reader()); err != nil {
			return err
		}
		if persistentBudget {
			sender := s.mailFrom
			if sender == "" {
				sender = fromAddr
			}
			limited, err := budget.ReserveSubmission(SubmissionAttempt{AccountID: s.accountID, CredentialID: s.credentialID, Sender: sender, HeaderSender: fromAddr, Recipients: int64(len(s.rcptTo)), Remote: s.remoteAddr()})
			if err != nil || limited {
				return &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 7, 1}, Message: "Sending temporarily held; retain this message and retry later"}
			}
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
		With:      receivedWith(isTLS, s.authenticated, s.smtpUTF8),
		ID:        rmail.GenerateQueueID(),
		For:       singleRecipient(s.rcptTo),
		Timestamp: time.Now(),
	})
	source := messageSource{head: received, body: message}
	// What stores or signs the message whole reads it from the file once, when a recipient first
	// needs it. A message that only reaches local mailboxes is never read into memory.
	wholeMessage := sync.OnceValues(source.whole)

	accepted := 0
	failed := 0
	failCode, failEnhanced, failMsg := 451, gosmtp.EnhancedCode{4, 3, 0}, "Temporary delivery failure"

	// DSN provenance: the first time this submission produces outbound mail we
	// persist a sender-owned reference for it and link every outbound queue row
	// to that reference, so a later bounce/DSN can be authenticated back to the
	// submitting mailbox. submittedMsgID stays nil when nothing was persisted.
	var submittedMsgID *uint
	submittedOnce := false

	// What a local recipient is delivered. Inbound, it is the message as
	// received, from the server that connected. An authenticated submission
	// comes from one of this server's own mailboxes, so a local recipient gets
	// it as one sent from the webmail (SendMessage): prepared as its remote
	// copies are (Bcc stripped, DKIM-signed), and with no client IP or HELO.
	// The client that submitted it is a mail program, not a server the
	// sender's SPF record lists: delivered unsigned from its address, the
	// message failed SPF and the domain's DMARC p=reject refused it (550),
	// however the session had signed in. Prepared once, at the first local
	// recipient.
	localSource, localClientIP, localHelo := source, extractIP(s.remoteAddr()), s.conn.Hostname()
	var preparedFile *spoolFile
	defer func() { preparedFile.remove() }()
	prepareLocal := s.isSubmission && s.authenticated
	var localErr error
	type recipientLookup struct {
		check *apiclient.MailboxCheckResponse
		err   error
	}
	lookups := make(map[string]recipientLookup, len(s.rcptTo))
	for _, rcpt := range s.rcptTo {
		if _, bounce := s.srsRelays[rcpt]; bounce {
			continue
		}
		check, err := s.api.CheckMailbox(rcpt)
		if reason, refused := aliasRoutingRefusal(err); refused {
			return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 1, 1}, Message: reason}
		}
		lookups[rcpt] = recipientLookup{check: check, err: err}
	}
	var aliasFailure string

	for _, rcpt := range s.rcptTo {
		// A bounce to a valid SRS address goes on to the sender it carries.
		if original, ok := s.srsRelays[rcpt]; ok {
			raw, err := wholeMessage()
			if err == nil {
				err = s.store.RelayBounce(BounceRelay{
					Recipient:       original,
					SendingDomain:   rmail.DomainFromAddress(rcpt),
					RawMessage:      raw,
					BodyType:        s.bodyType,
					MaxPerRecipient: s.srsBounceMax,
					Window:          s.srsWindow,
				})
			}
			if err != nil {
				slog.Error("smtp: failed to queue a bounce for an SRS address", "error", err)
				failed++
				continue
			}
			accepted++
			continue
		}

		// Check if this is a local recipient.
		check, err := lookups[rcpt].check, lookups[rcpt].err
		if err != nil {
			// Classification failed: we cannot tell whether this recipient is
			// local or remote. Do NOT fall through to the outbound queue — a
			// local recipient (this is the inbound-MX path) would then be
			// relayed externally (a leak) or MX-delivered back to ourselves.
			// Tempfail (451) so the sender retries once the API recovers,
			// matching the RCPT-time path. The default fail reply is already a
			// 451, so counting this recipient as failed yields the right reply
			// when nothing else commits.
			slog.Error("smtp: API error re-checking mailbox at DATA time; tempfailing to avoid misroute",
				"from", s.mailFrom, "to", rcpt, "error", err)
			failed++
			continue
		}
		if !check.Data.Exists {
			// A recipient accepted at RCPT can lose receive permission before
			// DATA. Inbound mail must never become an outbound relay then.
			if !(s.isSubmission && s.authenticated) {
				failCode, failEnhanced, failMsg = 550, gosmtp.EnhancedCode{5, 1, 1}, "No such user"
				failed++
				continue
			}
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
				raw, perr := wholeMessage()
				bodyText, textErr := parsed.text.whole()
				bodyHTML, htmlErr := parsed.html.whole()
				perr = errors.Join(perr, textErr, htmlErr)
				var ref *uint
				if perr == nil {
					ref, perr = s.store.PersistSubmittedMessage(SubmittedMessage{
						Sender:       s.mailFrom,
						MessageID:    messageID,
						SenderName:   senderName,
						Subject:      subject,
						BodyText:     bodyText,
						BodyHTML:     bodyHTML,
						InReplyTo:    inReplyTo,
						References:   references,
						RawMessage:   raw,
						RecipientsTo: toJSON,
						RecipientsCc: ccJSON,
					})
				}
				if perr != nil {
					slog.Warn("smtp: failed to persist submission reference for DSN provenance",
						"from", s.mailFrom, "error", perr)
				} else {
					submittedMsgID = ref
				}
			}

			raw, err := wholeMessage()
			if err == nil {
				err = s.store.EnqueueOutbound(OutboundMessage{
					Sender:     s.mailFrom,
					Recipient:  rcpt,
					Domain:     recipientDomain,
					RawMessage: raw,
					BodyType:   s.bodyType,
					MessageID:  submittedMsgID,
					RequireTLS: s.requireTLS,
				})
			}
			if err != nil {
				slog.Error("smtp: failed to queue message", "from", s.mailFrom, "to", rcpt, "error", err)
				failed++
				continue
			}
			slog.Info("smtp: queued for outbound delivery", "from", s.mailFrom, "to", rcpt)
			accepted++
			continue
		}

		if prepareLocal {
			prepareLocal = false
			localClientIP, localHelo = "", ""
			// The signed copy goes back to disk, so what follows reads it as it reads any
			// other message.
			file, err := s.prepareSubmitted(wholeMessage)
			if err != nil {
				// A configured DKIM key could not be loaded: defer, as the queue
				// worker does, rather than deliver the message unsigned.
				slog.Error("smtp: could not prepare a submission for local delivery; deferring",
					"from", s.mailFrom, "error", err)
				localErr = err
			} else {
				preparedFile = file
				localSource = messageSource{body: file}
			}
		}
		if localErr != nil {
			failCode, failEnhanced, failMsg = 451, gosmtp.EnhancedCode{4, 3, 0}, "Temporary delivery failure"
			failed++
			continue
		}

		// Local delivery via API.
		deliverReq := &apiclient.DeliverRequest{
			Address:    rcpt,
			Sender:     s.mailFrom,
			SenderName: senderName,
			Subject:    subject,
			MessageID:  messageID,
			InReplyTo:  inReplyTo,
			References: references,
			ClientIP:   localClientIP,
			HeloName:   localHelo,
			// Always-on inbound transport-security metrics (inbound-MX only).
			ReceivedTLS: receivedTLS,
			TLSVersion:  tlsVersion,
			TLSCipher:   tlsCipher,
			RequireTLS:  s.requireTLS,
		}
		if len(toList) > 0 {
			toJSON, _ := json.Marshal(toList)
			deliverReq.RecipientsTo = toJSON
		}
		if len(ccList) > 0 {
			ccJSON, _ := json.Marshal(ccList)
			deliverReq.RecipientsCc = ccJSON
		}

		if _, err := s.api.DeliverMessageFrom(deliverReq, apiclient.DeliverContent{
			RawMessage: localSource.open,
			BodyText:   parsed.text.opener(),
			BodyHTML:   parsed.html.opener(),
		}); err != nil {
			if reason, refused := aliasRoutingRefusal(err); refused {
				aliasFailure = reason
			}
			slog.Error("smtp: delivery failed", "from", s.mailFrom, "to", rcpt, "error", err)
			// Remember a representative reply for the all-failed case.
			var apiErr *apiclient.APIError
			if errors.As(err, &apiErr) {
				switch {
				case apiErr.StatusCode == 404:
					failCode, failEnhanced, failMsg = 550, gosmtp.EnhancedCode{5, 1, 1}, "No such user"
				case apiErr.StatusCode == 403 || apiErr.StatusCode == 550:
					failCode, failEnhanced, failMsg = 550, gosmtp.EnhancedCode{5, 7, 1}, "Rejected by policy"
				case apiErr.StatusCode == 422:
					// Recipient mailbox is over quota (the API answers HTTP 422
					// mailbox_full). Map to the RFC 3463 §3.2 mailbox-full status so
					// the sending MTA gets a machine-readable quota signal and retries
					// (the mailbox may be cleared), rather than an opaque 451 that
					// hides the cause. 452 4.2.2 is the transient form (retry); a
					// policy-permanent full mailbox would be 552 5.2.2.
					failCode, failEnhanced, failMsg = 452, gosmtp.EnhancedCode{4, 2, 2}, "Mailbox full"
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

	if aliasFailure != "" {
		return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 1, 1}, Message: aliasFailure}
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

// prepareSubmitted applies the outbound transforms to a submission for a local mailbox, and
// answers the result on disk. Signing is the one step of a local delivery that holds the whole
// message, because the DKIM signer takes it whole.
func (s *session) prepareSubmitted(whole func() (string, error)) (*spoolFile, error) {
	raw, err := whole()
	if err != nil {
		return nil, err
	}
	prepared, err := s.store.PrepareSubmitted(s.mailFrom, raw)
	if err != nil {
		return nil, err
	}
	return spool(strings.NewReader(prepared))
}

// aliasRoutingRefusal recognises the API's permanent refusal of a reserved alias that
// cannot route, which this gateway turns into SMTP 550. GET /api/mailboxes answers it 403
// and deliverMessage with its documented 550, both as rejected with the fixed sentence.
// Any other answer says nothing about the recipient: a 403 from anywhere else stays the
// temporary failure it was, so a fault here never bounces a sender's mail.
func aliasRoutingRefusal(err error) (string, bool) {
	var apiError *apiclient.APIError
	if !errors.As(err, &apiError) || (apiError.StatusCode != 403 && apiError.StatusCode != 550) {
		return "", false
	}
	var refusal struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(apiError.Body), &refusal) != nil || refusal.Error.Code != "rejected" || refusal.Error.Message != rmail.AliasRoutingRefusal {
		return "", false
	}
	return refusal.Error.Message, true
}

// Reset discards the current transaction (RSET, or implicitly after DATA).
func (s *session) Reset() {
	s.mailFrom = ""
	s.rcptTo = nil
	s.srsRelays = nil
	s.bodyType = ""
	s.smtpUTF8 = false
	s.requireTLS = false
}

// Logout is called when the connection is closed.
func (s *session) Logout() error {
	return nil
}
