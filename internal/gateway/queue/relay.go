package queue

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"gorm.io/gorm"
)

type relayTLSMode string

const (
	relaySTARTTLS    relayTLSMode = "starttls"
	relayImplicitTLS relayTLSMode = "implicit-tls"
)

// relaySettings describes an explicitly operator-selected endpoint, not an MX
// discovered from recipient input. relayRoute builds it from the sending
// domain's DomainRelay row, decrypting the password into memory only.
type relaySettings struct {
	host     string
	port     int
	mode     relayTLSMode
	username string
	password string
	// nil uses system trust. Custom roots still require hostname/chain verification.
	roots *x509.CertPool
}

func validRelayHost(host string) bool { return models.ValidRelayHost(host) }

func (s relaySettings) validate() error {
	if !validRelayHost(s.host) || s.port < 1 || s.port > 65535 {
		return errors.New("relay: invalid endpoint")
	}
	if s.mode != relaySTARTTLS && s.mode != relayImplicitTLS {
		return errors.New("relay: invalid TLS mode")
	}
	if (s.username == "") != (s.password == "") || rmail.ContainsControlChar(s.username) || rmail.ContainsControlChar(s.password) {
		return errors.New("relay: invalid credentials")
	}
	return nil
}

// relayFailure retains only SMTP status fields, never untrusted reply text:
// even an AUTH rejection can echo credentials or their base64 representation.
func relayFailure(ctx context.Context, stage, host string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("relay %s: %w", stage, ctx.Err())
	}
	var reply *textproto.Error
	if errors.As(err, &reply) {
		enhanced, _, _ := strings.Cut(strings.TrimSpace(reply.Msg), " ")
		if !isEnhancedStatus(enhanced) {
			enhanced = ""
		}
		return &SMTPError{Code: reply.Code, Enhanced: enhanced, Message: "relay " + stage + " rejected", RemoteMTA: host}
	}
	// The step's timer ran out on the socket. Say so in the error's own terms, as
	// its text is not kept.
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("relay %s: %w", stage, context.DeadlineExceeded)
	}
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed} {
		if errors.Is(err, safe) {
			return fmt.Errorf("relay %s: %w", stage, safe)
		}
	}
	return fmt.Errorf("relay %s failed", stage)
}

// deliverToRelay submits one already-prepared, CRLF-terminated message without
// signing, stripping headers or choosing a route. The envelope is the queue
// row's envelope, including an empty reverse path, with a forward's sender
// rewritten (forwardSender). No direct-MX or RESTMAIL
// fallback is permitted. AUTH PLAIN is optional, but only over verified TLS.
func (w *Worker) deliverToRelay(ctx context.Context, settings relaySettings, item models.OutboundQueue) error {
	if err := settings.validate(); err != nil {
		return err
	}
	if rmail.ContainsControlChar(item.Sender) || rmail.ContainsControlChar(item.Recipient) {
		return &SMTPError{Code: 550, Enhanced: "5.1.3", Message: "address contains control characters"}
	}
	if !validRelayHost(w.hostname) {
		return errors.New("relay: invalid EHLO hostname")
	}
	envelopeSender, err := w.forwardSender(item)
	if err != nil {
		return err
	}
	item.Sender = envelopeSender
	sender, recipient, err := smtpEnvelope(item)
	if err != nil {
		return err
	}
	// The same timers as direct delivery (#714), and nothing times the attempt as a
	// whole. A caller's own context still ends it at any step: closing the socket
	// interrupts them all, including StartTLS and net/smtp.Auth, which do not accept
	// a context.
	timeouts := w.timeouts()
	fail := func(stage string, err error) error { return relayFailure(ctx, stage, settings.host, err) }
	conn, err := (&net.Dialer{Timeout: smtpDialTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(settings.host, strconv.Itoa(settings.port)))
	if err != nil {
		return fail("connect", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: settings.host, RootCAs: settings.roots, MinVersion: tls.VersionTLS12}
	var transport net.Conn = conn
	if settings.mode == relayImplicitTLS {
		armStep(conn, timeouts.StartTLS)
		secure := tls.Client(conn, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return fail("TLS handshake", err)
		}
		transport = secure
	}
	armStep(conn, timeouts.Greeting)
	client, err := smtp.NewClient(transport, settings.host)
	if err != nil {
		return fail("greeting", err)
	}
	defer func() { _ = client.Close() }()
	armStep(conn, timeouts.Hello)
	if err := client.Hello(w.hostname); err != nil {
		return fail("EHLO", err)
	}
	if settings.mode == relaySTARTTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("relay: required STARTTLS unavailable")
		}
		armAfterHandshake(tlsConfig, conn, timeouts.Hello)
		armStep(conn, timeouts.StartTLS)
		if err := client.StartTLS(tlsConfig); err != nil {
			return fail("STARTTLS", err)
		}
	}
	if state, ok := client.TLSConnectionState(); !ok || !state.HandshakeComplete || len(state.VerifiedChains) == 0 {
		return errors.New("relay: verified TLS required")
	}
	// A message that requires TLS goes on only to a relay that carries the requirement
	// further (RFC 8689 §4.2.1): one that offers REQUIRETLS, over the verified TLS above.
	if item.RequireTLS && item.Sender != "" {
		if ok, _ := client.Extension("REQUIRETLS"); !ok {
			return &SMTPError{Code: 550, Enhanced: "5.7.30", RemoteMTA: settings.host,
				Message: "REQUIRETLS support required: the relay " + settings.host + " does not offer REQUIRETLS"}
		}
	}
	if settings.username != "" {
		if ok, mechanisms := client.Extension("AUTH"); !ok || !relayHasMechanism(mechanisms, "PLAIN") {
			return errors.New("relay: AUTH PLAIN unavailable")
		}
		armStep(conn, timeouts.Auth)
		if err := client.Auth(smtp.PlainAuth("", settings.username, settings.password, settings.host)); err != nil {
			return fail("AUTH", err)
		}
	}
	if ok, _ := client.Extension("8BITMIME"); !ok && messageRequires8BitTransport(item.BodyType, item.RawMessage) {
		return &SMTPError{Code: 550, Enhanced: "5.6.3", Message: "relay requires 8BITMIME", RemoteMTA: settings.host}
	}
	if err := checkSMTPUTF8(client, sender, recipient, item.RawMessage, settings.host); err != nil {
		return err
	}
	armStep(conn, timeouts.Mail)
	if err := mailFrom(client, sender, smtpUTF8Status(sender, recipient, item.RawMessage) != "", item.RequireTLS && item.Sender != ""); err != nil {
		return fail("MAIL", err)
	}
	armStep(conn, timeouts.Rcpt)
	if err := client.Rcpt(recipient); err != nil {
		return fail("RCPT", err)
	}
	armStep(conn, timeouts.Data)
	data, err := client.Data()
	if err != nil {
		return fail("DATA", err)
	}
	if err := writeDataBlocks(data, client.Text.W.Flush, item.RawMessage, func() { armStep(conn, timeouts.DataBlock) }); err != nil {
		return fail("DATA write", err)
	}
	armStep(conn, timeouts.FinalReply)
	if err := data.Close(); err != nil {
		return fail("DATA reply", err)
	}
	// The final DATA acceptance is the delivery result; a failed QUIT must not
	// cause an accepted message to be retried. Closing releases the socket.
	return nil
}

func relayHasMechanism(list, want string) bool {
	for _, mechanism := range strings.Fields(list) {
		if strings.EqualFold(mechanism, want) {
			return true
		}
	}
	return false
}

// relayRoute returns the relay a message must go through, or nil when its
// sender's domain sends direct (#143). Anything short of a clear answer — the
// settings cannot be read, or the stored password cannot be decrypted — is an
// error, so the message is retried rather than sent direct or unauthenticated.
func (w *Worker) relayRoute(item models.OutboundQueue) (*relaySettings, error) {
	domain := sendsAs(item)
	if domain == "" {
		return nil, nil
	}
	var rel models.DomainRelay
	err := w.db.Joins("JOIN domains ON domains.id = domain_relays.domain_id").
		Where("LOWER(domains.name) = LOWER(?)", domain).
		First(&rel).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("relay: load settings for %s: %w", domain, err)
	}
	if rel.Transport != models.OutboundTransportRelay {
		return nil, nil
	}
	password, err := models.LoadRelayPassword(rel.PasswordEnc, w.masterKey)
	if err != nil {
		return nil, fmt.Errorf("relay: credentials for %s: %w", domain, err)
	}
	mode := relaySTARTTLS
	if rel.TLSMode == models.RelayTLSImplicit {
		mode = relayImplicitTLS
	} else if rel.TLSMode != models.RelayTLSStartTLS {
		return nil, fmt.Errorf("relay: invalid TLS mode for %s", domain)
	}
	return &relaySettings{
		host:     rel.Host,
		port:     rel.Port,
		mode:     mode,
		username: rel.Username,
		password: password,
		roots:    w.relayRoots,
	}, nil
}

// deliverViaRelay prepares the message once, exactly as the direct path does,
// and hands it to the relay. There is no RESTMAIL probe and no fallback to MX.
func (w *Worker) deliverViaRelay(settings relaySettings, item models.OutboundQueue) error {
	normal, err := forData(item.RawMessage, settings.host)
	if err != nil {
		return err
	}
	item.RawMessage = normal
	prepared, err := w.prepareOutbound(item)
	if err != nil {
		return fmt.Errorf("prepare outbound message: %w", err)
	}
	item.RawMessage = prepared
	return w.deliverToRelay(context.Background(), settings, item)
}
