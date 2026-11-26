package imapmail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	mail "github.com/emersion/go-message/mail"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
)

func (store *Store) smtp(ctx context.Context, account Account, password string) (*smtp.Client, func(), error) {
	config, err := store.tls(account.SMTPHost)
	if err != nil {
		return nil, nil, err
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	address := net.JoinHostPort(account.SMTPHost, strconv.Itoa(account.SMTPPort))
	var conn net.Conn
	if account.SMTPSecurity == "tls" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: config}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, nil, errors.New("SMTP connection with verified TLS failed")
	}
	_ = conn.SetDeadline(deadline(ctx))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cleanup := func() { stop(); _ = conn.Close() }
	client, err := smtp.NewClient(conn, account.SMTPHost)
	if err == nil && account.SMTPSecurity == "starttls" {
		err = client.StartTLS(config)
	}
	if err != nil {
		cleanup()
		return nil, nil, errors.New("SMTP requires verified TLS")
	}
	state, secure := client.TLSConnectionState()
	if !secure || !state.HandshakeComplete || len(state.VerifiedChains) == 0 {
		cleanup()
		return nil, nil, errors.New("SMTP requires a verified TLS connection")
	}
	ok, mechanisms := client.Extension("AUTH")
	var auth smtp.Auth
	if ok {
		for _, mechanism := range strings.Fields(mechanisms) {
			if mechanism == "PLAIN" {
				auth = smtp.PlainAuth("", account.Username, password, account.SMTPHost)
				break
			}
			if mechanism == "LOGIN" {
				auth = &loginAuth{username: account.Username, password: password}
			}
		}
	}
	if auth == nil || client.Auth(auth) != nil {
		cleanup()
		return nil, nil, errors.New("SMTP authentication failed")
	}
	return client, cleanup, nil
}

type loginAuth struct {
	username, password string
	step               int
}

func (auth *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS {
		return "", nil, errors.New("SMTP AUTH requires TLS")
	}
	auth.step = 0
	return "LOGIN", nil, nil
}
func (auth *loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	auth.step++
	switch auth.step {
	case 1:
		return []byte(auth.username), nil
	case 2:
		return []byte(auth.password), nil
	}
	return nil, errors.New("unexpected SMTP authentication challenge")
}

func (store *Store) checkSMTP(ctx context.Context, account Account, password string) error {
	client, cleanup, err := store.smtp(ctx, account, password)
	if err != nil {
		return err
	}
	defer cleanup()
	if client.Quit() != nil {
		return errors.New("SMTP connection failed")
	}
	return nil
}

func buildMessage(account Account, request apitypes.SendMessageRequest) ([]byte, []string, error) {
	if request.From != "" && !strings.EqualFold(request.From, account.Address) {
		return nil, nil, errors.New("sender must match IMAP account address")
	}
	if request.OnBehalf || request.RequireTLS || request.ScheduledAt != nil || request.CalendarEvent != nil || request.ListUnsubscribe != nil {
		return nil, nil, errors.New("requested sending feature is not supported for IMAP accounts")
	}
	for _, value := range append(append(append([]string{request.Subject, request.InReplyTo}, request.To...), request.Cc...), request.Bcc...) {
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, nil, errors.New("invalid outgoing message header")
		}
	}
	var header mail.Header
	header.SetAddressList("From", []*stdmail.Address{{Address: account.Address}})
	recipients := []string{}
	for _, group := range []struct {
		name      string
		addresses []string
	}{{"To", request.To}, {"Cc", request.Cc}, {"Bcc", request.Bcc}} {
		parsed := []*stdmail.Address{}
		for _, value := range group.addresses {
			address, err := stdmail.ParseAddress(value)
			if err != nil {
				return nil, nil, errors.New("invalid recipient address")
			}
			parsed = append(parsed, address)
			recipients = append(recipients, address.Address)
		}
		if group.name != "Bcc" && len(parsed) > 0 {
			header.SetAddressList(group.name, parsed)
		}
	}
	if len(recipients) == 0 {
		return nil, nil, errors.New("at least one recipient is required")
	}
	header.SetSubject(request.Subject)
	header.SetDate(time.Now())
	identity := make([]byte, 16)
	if _, err := rand.Read(identity); err != nil {
		return nil, nil, errors.New("could not create message identity")
	}
	header.Set("Message-ID", "<"+hex.EncodeToString(identity)+"@"+strings.Split(account.Address, "@")[1]+">")
	if request.InReplyTo != "" {
		header.Set("In-Reply-To", request.InReplyTo)
	}
	var output bytes.Buffer
	writer, err := mail.CreateInlineWriter(&output, header)
	if err != nil {
		return nil, nil, errors.New("could not encode outgoing message")
	}
	for _, body := range []struct{ kind, content string }{{"text/plain", request.BodyText}, {"text/html", request.BodyHTML}} {
		if body.content == "" && body.kind == "text/html" {
			continue
		}
		var partHeader mail.InlineHeader
		partHeader.Set("Content-Type", body.kind+"; charset=utf-8")
		part, err := writer.CreatePart(partHeader)
		if err != nil {
			return nil, nil, errors.New("could not encode outgoing body")
		}
		if _, err = io.WriteString(part, body.content); err != nil {
			return nil, nil, errors.New("could not encode outgoing body")
		}
		if part.Close() != nil {
			return nil, nil, errors.New("could not finish outgoing body")
		}
	}
	if writer.Close() != nil || output.Len() > maxMessageSize {
		return nil, nil, errors.New("outgoing message is too large")
	}
	return output.Bytes(), recipients, nil
}

func (store *Store) SendMessage(ctx context.Context, account Account, request apitypes.SendMessageRequest) error {
	raw, recipients, err := buildMessage(account, request)
	if err != nil {
		return err
	}
	password, err := keyringPassword(account.ID)
	if err != nil {
		return err
	}
	store.remember(password)
	client, cleanup, err := store.smtp(ctx, account, password)
	if err != nil {
		return err
	}
	defer cleanup()
	if client.Mail(account.Address) != nil {
		return errors.New("SMTP sender refused")
	}
	for _, recipient := range recipients {
		if client.Rcpt(recipient) != nil {
			return errors.New("SMTP recipient refused")
		}
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP message refused")
	}
	if _, err = writer.Write(raw); err != nil {
		return errors.New("SMTP message transfer failed; delivery status unknown")
	}
	if writer.Close() != nil {
		return errors.New("SMTP message acceptance failed; delivery status unknown")
	}
	_ = client.Quit()
	return nil
}
