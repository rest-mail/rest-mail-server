package queue

import (
	"bufio"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rest-mail/go-mtasts"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/metrics"
)

func tlsTestItem() models.OutboundQueue {
	return models.OutboundQueue{Sender: "sender@example.test", Recipient: "user@example.test", Domain: "example.test",
		RawMessage: "Subject: opportunistic\r\n\r\nhello\r\n"}
}

func transportCount(security string) float64 {
	return testutil.ToFloat64(metrics.OutboundTransport.WithLabelValues(security))
}

// An MX with a certificate nothing vouches for, and no MTA-STS policy, gets the
// message over TLS, recorded as encrypted, not authenticated (RFC 7435 §3, #723).
func TestDeliverSMTP_UnverifiedCertificateStillEncrypts(t *testing.T) {
	cert, _ := relayTestCertificate(t, false) // self-signed: not in the system roots
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{noAUTH: true})
	w := &Worker{hostname: "sender.test", allowPrivateDest: true, sendDeadline: defaultSendDeadline}
	encrypted, authenticated := transportCount(transportEncrypted), transportCount(transportAuthenticated)

	if err := w.deliverSMTP(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, tlsTestItem(), nil, nil); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	result := finish()
	if !slices.Contains(result.commands, "STARTTLS") || !strings.Contains(result.data, "hello") {
		t.Fatalf("not delivered over TLS: commands %v, data %q", result.commands, result.data)
	}
	if got := transportCount(transportEncrypted) - encrypted; got != 1 {
		t.Errorf("encrypted deliveries rose by %v, want 1", got)
	}
	if got := transportCount(transportAuthenticated) - authenticated; got != 0 {
		t.Errorf("an unverified certificate was counted as authenticated (%v)", got)
	}
}

// brokenTLSMX advertises STARTTLS and, on the first connection, answers it
// with something that is not a TLS handshake; it takes the message on the
// next, and reports that connection's commands.
func brokenTLSMX(t *testing.T) (host string, port int, finish func() []string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan []string, 1)
	go func() {
		var second []string
		defer func() { done <- second }()
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			r, wr := bufio.NewReader(conn), bufio.NewWriter(conn)
			reply := func(s string) { _, _ = wr.WriteString(s + "\r\n"); _ = wr.Flush() }
			reply("220 loopback ready")
			inData := false
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					break
				}
				line = strings.TrimRight(line, "\r\n")
				if inData {
					if line == "." {
						inData = false
						reply("250 queued")
					}
					continue
				}
				if i == 1 {
					second = append(second, line)
				}
				switch cmd, _, _ := strings.Cut(line, " "); strings.ToUpper(cmd) {
				case "EHLO":
					reply("250-loopback")
					reply("250-8BITMIME")
					reply("250 STARTTLS")
				case "STARTTLS":
					reply("220 go ahead")
					reply("this is not a TLS handshake")
				case "DATA":
					inData = true
					reply("354 go on")
				case "QUIT":
					reply("221 bye")
				default:
					reply("250 ok")
				}
				if strings.EqualFold(line, "STARTTLS") || strings.EqualFold(line, "QUIT") {
					break
				}
			}
			_ = conn.Close()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, func() []string {
		select {
		case c := <-done:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("the MX did not finish")
			return nil
		}
	}
}

// An MX whose TLS handshake fails, under no policy, gets the message on a new
// connection without TLS, recorded as cleartext; nothing is sent on the
// broken one (#723).
func TestDeliverSMTP_FailedHandshakeRetriesInCleartext(t *testing.T) {
	host, port, finish := brokenTLSMX(t)
	w := &Worker{hostname: "sender.test", allowPrivateDest: true, sendDeadline: defaultSendDeadline}
	cleartext := transportCount(transportCleartext)

	if err := w.deliverSMTP(net.JoinHostPort(host, strconv.Itoa(port)), host, tlsTestItem(), nil, nil); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	second := finish()
	if slices.Contains(second, "STARTTLS") || !slices.ContainsFunc(second, func(c string) bool { return strings.HasPrefix(c, "MAIL FROM:") }) {
		t.Fatalf("the retry should send the message without STARTTLS: %v", second)
	}
	if got := transportCount(transportCleartext) - cleartext; got != 1 {
		t.Errorf("cleartext deliveries rose by %v, want 1", got)
	}
}

// Under an MTA-STS enforce policy nothing changes: an unverified certificate,
// or a failed handshake, defers the message and sends nothing (RFC 8461 §5).
func TestDeliverSMTP_EnforceStillRequiresAVerifiedCertificate(t *testing.T) {
	cert, _ := relayTestCertificate(t, false)
	for _, opts := range []relayPeerOptions{{noAUTH: true}, {noAUTH: true, breakTLS: true}} {
		settings, finish := relayTestPeer(t, cert, opts)
		w := &Worker{hostname: "sender.test", allowPrivateDest: true, sendDeadline: defaultSendDeadline, stsEnforce: true}
		policy := &mtasts.Policy{Mode: mtasts.ModeEnforce, MX: []string{settings.host}, MaxAge: 86400}
		err := w.deliverSMTP(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, tlsTestItem(), policy, nil)
		var enforced *mtasts.EnforceError
		if !errors.As(err, &enforced) {
			t.Fatalf("breakTLS=%v: %v, want an MTA-STS enforce refusal", opts.breakTLS, err)
		}
		if result := finish(); slices.ContainsFunc(result.commands, func(c string) bool { return strings.HasPrefix(c, "MAIL") }) {
			t.Fatalf("breakTLS=%v: a message was started: %v", opts.breakTLS, result.commands)
		}
	}
}
