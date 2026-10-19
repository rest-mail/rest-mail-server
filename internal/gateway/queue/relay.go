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
	"time"
	"unicode/utf8"

	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
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
	// Socket deadlines can fire just before the context's timer callback.
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return fmt.Errorf("relay %s: %w", stage, context.DeadlineExceeded)
		}
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
// row's envelope, including an empty reverse path. No direct-MX or RESTMAIL
// fallback is permitted. AUTH PLAIN is optional, but only over verified TLS.
func (w *Worker) deliverToRelay(parent context.Context, settings relaySettings, item models.OutboundQueue) error {
	if err := settings.validate(); err != nil {
		return err
	}
	if rmail.ContainsControlChar(item.Sender) || rmail.ContainsControlChar(item.Recipient) {
		return &SMTPError{Code: 550, Enhanced: "5.1.3", Message: "address contains control characters"}
	}
	if !validRelayHost(w.hostname) {
		return errors.New("relay: invalid EHLO hostname")
	}
	if !utf8.ValidString(item.Sender) || !utf8.ValidString(item.Recipient) {
		return errors.New("relay: invalid envelope encoding")
	}
	budget := w.sendDeadline
	if budget == nil {
		budget = defaultSendDeadline
	}
	ctx, cancel := context.WithTimeout(parent, budget(int64(len(item.RawMessage))))
	defer cancel()
	fail := func(stage string, err error) error { return relayFailure(ctx, stage, settings.host, err) }
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(settings.host, strconv.Itoa(settings.port)))
	if err != nil {
		return fail("connect", err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return fail("deadline", err)
	}
	// Closing the underlying socket interrupts every SMTP phase on cancellation,
	// including StartTLS and net/smtp.Auth, which do not accept a context.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: settings.host, RootCAs: settings.roots, MinVersion: tls.VersionTLS12}
	var transport net.Conn = conn
	if settings.mode == relayImplicitTLS {
		secure := tls.Client(conn, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return fail("TLS handshake", err)
		}
		transport = secure
	}
	client, err := smtp.NewClient(transport, settings.host)
	if err != nil {
		return fail("greeting", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Hello(w.hostname); err != nil {
		return fail("EHLO", err)
	}
	if settings.mode == relaySTARTTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("relay: required STARTTLS unavailable")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fail("STARTTLS", err)
		}
	}
	if state, ok := client.TLSConnectionState(); !ok || !state.HandshakeComplete || len(state.VerifiedChains) == 0 {
		return errors.New("relay: verified TLS required")
	}
	if settings.username != "" {
		if ok, mechanisms := client.Extension("AUTH"); !ok || !relayHasMechanism(mechanisms, "PLAIN") {
			return errors.New("relay: AUTH PLAIN unavailable")
		}
		if err := client.Auth(smtp.PlainAuth("", settings.username, settings.password, settings.host)); err != nil {
			return fail("AUTH", err)
		}
	}
	if ok, _ := client.Extension("8BITMIME"); !ok && messageRequires8BitTransport(item.BodyType, item.RawMessage) {
		return &SMTPError{Code: 550, Enhanced: "5.6.3", Message: "relay requires 8BITMIME", RemoteMTA: settings.host}
	}
	headers, _, _ := strings.Cut(item.RawMessage, "\r\n\r\n")
	if ok, _ := client.Extension("SMTPUTF8"); !ok && relayNonASCII(item.Sender+item.Recipient+headers) {
		return &SMTPError{Code: 550, Enhanced: "5.6.7", Message: "relay requires SMTPUTF8", RemoteMTA: settings.host}
	}
	if err := client.Mail(item.Sender); err != nil {
		return fail("MAIL", err)
	}
	if err := client.Rcpt(item.Recipient); err != nil {
		return fail("RCPT", err)
	}
	data, err := client.Data()
	if err != nil {
		return fail("DATA", err)
	}
	if _, err := io.WriteString(data, item.RawMessage); err != nil {
		return fail("DATA write", err)
	}
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

func relayNonASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
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
	domain := rmail.DomainFromAddress(item.Sender)
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
	prepared, err := w.prepareOutbound(item)
	if err != nil {
		return fmt.Errorf("prepare outbound message: %w", err)
	}
	item.RawMessage = prepared
	return w.deliverToRelay(context.Background(), settings, item)
}
