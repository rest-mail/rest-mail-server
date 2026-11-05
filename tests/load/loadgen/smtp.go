package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/rest-mail/go-smtp/pkg/smtp/client"
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
	// run tags every message for the sink, which counts only its own run.
	run     string
	sources *sourcePool
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
	fs.StringVar(&cfg.run, "run", "", "tag messages with this run id, for loadgen sink -run")
	sources := fs.String("source-ips", "", "connect from these local addresses in turn, as 10.99.201.1-25")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var err error
	if cfg.sources, err = parseSources(*sources); err != nil {
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
		conn net.Conn
		err  error
	)
	d := cfg.sources.dialer(cfg.timeout)
	if cfg.implicitTLS {
		tlsDialer := tls.Dialer{NetDialer: d, Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // the testbed's certificates come from its own CA
		conn, err = tlsDialer.Dial("tcp", cfg.addr)
	} else {
		conn, err = d.Dial("tcp", cfg.addr)
	}
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	c := client.New(conn)
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
// unique Message-ID, so no server can deduplicate the load away. With cfg.run it
// also says which run sent it and when, which is what the sink measures from.
func message(cfg smtpConfig) string {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	var b strings.Builder
	fmt.Fprintf(&b, "From: <%s>\r\nTo: <%s>\r\nSubject: load %x\r\n", cfg.from, cfg.to, id)
	fmt.Fprintf(&b, "Date: %s\r\nMessage-ID: <%s@loadgen.test>\r\n", time.Now().UTC().Format(time.RFC1123Z), hex.EncodeToString(id))
	if cfg.run != "" {
		fmt.Fprintf(&b, "%s: %s\r\n%s: %d\r\n", headerRun, cfg.run, headerSent, time.Now().UnixNano())
	}
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=us-ascii\r\n\r\n")
	line := strings.Repeat("x", 76) + "\r\n"
	for n := 0; n < cfg.size; n += len(line) {
		b.WriteString(line)
	}
	return b.String()
}
