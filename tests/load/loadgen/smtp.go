package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/rest-mail/go-smtp/pkg/smtp/client"
	smtpserver "github.com/rest-mail/go-smtp/pkg/smtp/server"

	gosmtp "github.com/rest-mail/go-smtp/pkg/smtp"
)

type smtpConfig struct {
	addr        string
	implicitTLS bool
	user        string
	pass        string
	helo        string
	from        string
	to          string
	size        int
	timeout     time.Duration
}

// runSMTP sends one message per connection, the way a remote MTA (or
// smtp-source -s 1) does. With -user it signs in first, which is a submission.
func runSMTP(args []string) error {
	fs := flag.NewFlagSet("smtp", flag.ExitOnError)
	var cfg smtpConfig
	kind := fs.String("kind", "smtp_in", "name of this load in the results")
	rate := fs.Float64("rate", 100, "messages per second")
	duration := fs.Duration("duration", time.Minute, "how long to send")
	inflight := fs.Int("max-inflight", 500, "most messages in flight at once")
	out := fs.String("out", "-", "result file (- for stdout)")
	fs.StringVar(&cfg.addr, "addr", "restmail.test:25", "SMTP server host:port")
	fs.BoolVar(&cfg.implicitTLS, "tls", false, "implicit TLS (port 465)")
	fs.StringVar(&cfg.user, "user", "", "sign in as this user (submission)")
	fs.StringVar(&cfg.pass, "pass", "", "password for -user")
	fs.StringVar(&cfg.helo, "helo", "loadgen.test", "EHLO name")
	fs.StringVar(&cfg.from, "from", "load@loadgen.test", "envelope sender")
	fs.StringVar(&cfg.to, "to", "frank@restmail.test", "envelope recipient")
	fs.IntVar(&cfg.size, "size", 4096, "approximate message body size in bytes")
	fs.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "per-message timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rec := newRecorder()
	elapsed := paced(context.Background(), *rate, *duration, *inflight, rec, func(context.Context) error {
		return sendOne(cfg)
	})
	return writeResult(*out, rec.result(*kind, *rate, "msg/s", elapsed))
}

func sendOne(cfg smtpConfig) error {
	var (
		c   *client.Client
		err error
	)
	tlsConfig := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the testbed's certificates come from its own CA
	if cfg.implicitTLS {
		c, err = client.DialTLS(cfg.addr, tlsConfig)
	} else {
		c, err = client.Dial(cfg.addr)
	}
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = c.Close() }()
	c.CommandTimeout = cfg.timeout
	c.SubmissionTimeout = cfg.timeout
	if err := c.Hello(cfg.helo); err != nil {
		return fmt.Errorf("ehlo: %w", err)
	}
	if cfg.user != "" {
		if err := c.Auth(sasl.NewPlainClient("", cfg.user, cfg.pass)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(cfg.from, nil); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(cfg.to, nil); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := io.WriteString(w, message(cfg)); err != nil {
		return fmt.Errorf("data write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("data end: %w", err)
	}
	return c.Quit()
}

// message builds a plain RFC 5322 message of about cfg.size bytes with a
// unique Message-ID, so no server can deduplicate the load away.
func message(cfg smtpConfig) string {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	var b strings.Builder
	fmt.Fprintf(&b, "From: <%s>\r\nTo: <%s>\r\nSubject: load %x\r\n", cfg.from, cfg.to, id)
	fmt.Fprintf(&b, "Date: %s\r\nMessage-ID: <%s@loadgen.test>\r\n", time.Now().UTC().Format(time.RFC1123Z), hex.EncodeToString(id))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=us-ascii\r\n\r\n")
	line := strings.Repeat("x", 76) + "\r\n"
	for n := 0; n < cfg.size; n += len(line) {
		b.WriteString(line)
	}
	return b.String()
}

// runSink is an SMTP server that accepts and discards everything: a remote MX
// for outbound load, and the other end of the harness's own smoke test.
func runSink(args []string) error {
	fs := flag.NewFlagSet("sink", flag.ExitOnError)
	addr := fs.String("addr", ":2525", "listen address")
	duration := fs.Duration("duration", time.Minute, "how long to listen")
	out := fs.String("out", "-", "result file (- for stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, count := newSink(*addr)
	go func() { _ = s.ListenAndServe() }()
	time.Sleep(*duration)
	_ = s.Close()
	n := count.Load()
	return writeResult(*out, Result{
		Kind: "sink", DurationSec: duration.Seconds(), OK: n, Attempted: n,
		AchievedRate: round(float64(n) / duration.Seconds()), TargetUnit: "msg/s",
	})
}

func newSink(addr string) (*smtpserver.Server, *atomic.Int64) {
	var count atomic.Int64
	s := smtpserver.New(smtpserver.BackendFunc(func(*smtpserver.Conn) (smtpserver.Session, error) {
		return &sinkSession{count: &count}, nil
	}))
	s.Addr = addr
	s.Domain = "sink.loadgen.test"
	s.ReadTimeout = time.Minute
	s.WriteTimeout = time.Minute
	s.MaxMessageBytes = 64 << 20
	s.AllowInsecureAuth = true
	return s, &count
}

type sinkSession struct{ count *atomic.Int64 }

func (s *sinkSession) Reset()                                 {}
func (s *sinkSession) Logout() error                          { return nil }
func (s *sinkSession) Mail(string, *gosmtp.MailOptions) error { return nil }
func (s *sinkSession) Rcpt(string, *gosmtp.RcptOptions) error { return nil }
func (s *sinkSession) AuthMechanisms() []string               { return []string{sasl.Plain} }
func (s *sinkSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(string, string, string) error { return nil }), nil
}
func (s *sinkSession) Data(r io.Reader) error {
	if _, err := io.Copy(io.Discard, r); err != nil {
		return err
	}
	s.count.Add(1)
	return nil
}
