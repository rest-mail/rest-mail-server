package queue

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

const relayTestUser = "synthetic-user"
const relayTestPassword = "synthetic-secret-never-log"

func relayTestCertificate(t *testing.T, mismatch bool) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ip := "127.0.0.1"
	if mismatch {
		ip = "127.0.0.2"
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "loopback relay"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP(ip)}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

type relayPeerOptions struct {
	implicit       bool
	noSTARTTLS     bool
	breakTLS       bool
	noAUTH         bool
	no8BIT         bool
	noUTF8         bool
	requireAUTH    bool
	rejectAt       string
	reply          string
	silentAt       string
	preTLSAUTH     bool
	authChallenge  string
	authMechanisms string
}

type relayPeerResult struct {
	commands      []string
	data          string
	auth          string
	authBeforeTLS bool
	err           error
}

func relayTestPeer(t *testing.T, cert tls.Certificate, opts relayPeerOptions) (relaySettings, func() relayPeerResult) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan relayPeerResult, 1)
	go func() {
		result := relayPeerResult{}
		defer func() { done <- result }()
		conn, err := listener.Accept()
		if err != nil {
			result.err = err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		tlsActive := false
		if opts.implicit {
			if opts.silentAt == "TLS handshake" {
				_, _ = io.Copy(io.Discard, conn)
				return
			}
			secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
			if err := secure.Handshake(); err != nil {
				return
			}
			conn = secure
			tlsActive = true
		}
		if opts.silentAt == "greeting" {
			_, _ = io.Copy(io.Discard, conn)
			return
		}
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		reply := func(s string) bool {
			if _, err := writer.WriteString(s + "\r\n"); err != nil {
				return false
			}
			return writer.Flush() == nil
		}
		if opts.rejectAt == "greeting" {
			reply(opts.reply)
			return
		}
		if !reply("220 loopback ready") {
			return
		}
		authenticated := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			result.commands = append(result.commands, line)
			command, _, _ := strings.Cut(line, " ")
			if opts.silentAt == command {
				_, _ = io.Copy(io.Discard, conn)
				return
			}
			if opts.rejectAt == command {
				if !reply(opts.reply) {
					return
				}
				continue
			}
			switch command {
			case "EHLO":
				caps := []string{"loopback"}
				if !tlsActive && !opts.noSTARTTLS {
					caps = append(caps, "STARTTLS")
				}
				if tlsActive && !opts.noAUTH || !tlsActive && opts.preTLSAUTH {
					mechanisms := opts.authMechanisms
					if mechanisms == "" {
						mechanisms = "PLAIN"
					}
					caps = append(caps, "AUTH "+mechanisms)
				}
				if !opts.no8BIT {
					caps = append(caps, "8BITMIME")
				}
				if !opts.noUTF8 {
					caps = append(caps, "SMTPUTF8")
				}
				for i, capability := range caps {
					sep := "-"
					if i == len(caps)-1 {
						sep = " "
					}
					if !reply("250" + sep + capability) {
						return
					}
				}
			case "STARTTLS":
				if !reply("220 upgrade") {
					return
				}
				if opts.silentAt == "TLS handshake" {
					_, _ = io.Copy(io.Discard, conn)
					return
				}
				if opts.breakTLS {
					reply("not a TLS handshake")
					return
				}
				secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if err := secure.Handshake(); err != nil {
					return
				}
				conn = secure
				reader = bufio.NewReader(conn)
				writer = bufio.NewWriter(conn)
				tlsActive = true
			case "AUTH":
				result.authBeforeTLS = !tlsActive
				fields := strings.Fields(line)
				if len(fields) != 3 {
					result.err = fmt.Errorf("unexpected AUTH shape")
					return
				}
				decoded, err := base64.StdEncoding.DecodeString(fields[2])
				if err != nil {
					result.err = err
					return
				}
				result.auth = string(decoded)
				if opts.authChallenge != "" {
					if !reply("334 " + opts.authChallenge) {
						return
					}
					continue
				}
				authenticated = true
				if !reply("235 authenticated") {
					return
				}
			case "MAIL":
				if opts.requireAUTH && !authenticated {
					if !reply("530 5.7.0 authentication required") {
						return
					}
					continue
				}
				if !reply("250 sender accepted") {
					return
				}
			case "RCPT":
				if !reply("250 recipient accepted") {
					return
				}
			case "DATA":
				if !reply("354 send message") {
					return
				}
				if opts.silentAt == "DATA write" {
					// Do not drain the socket; a large DATA write must observe the
					// attempt budget even when the peer stops reading.
					timer := time.NewTimer(250 * time.Millisecond)
					defer timer.Stop()
					<-timer.C
					return
				}
				var raw strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					if strings.HasPrefix(line, ".") {
						line = line[1:]
					}
					raw.WriteString(line)
				}
				result.data = raw.String()
				if opts.silentAt == "DATA reply" {
					_, _ = io.Copy(io.Discard, conn)
					return
				}
				if opts.rejectAt == "DATA reply" {
					if !reply(opts.reply) {
						return
					}
				} else if !reply("250 2.0.0 accepted") {
					return
				}
			case "*":
				if !reply("501 canceled") {
					return
				}
			case "QUIT":
				reply("221 bye")
				return
			default:
				result.err = fmt.Errorf("unexpected command %q", command)
				return
			}
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	mode := relaySTARTTLS
	if opts.implicit {
		mode = relayImplicitTLS
	}
	settings := relaySettings{host: "127.0.0.1", port: address.Port, mode: mode, username: relayTestUser, password: relayTestPassword}
	return settings, func() relayPeerResult {
		t.Helper()
		select {
		case result := <-done:
			if result.err != nil {
				t.Fatal(result.err)
			}
			return result
		case <-time.After(4 * time.Second):
			t.Fatal("loopback peer did not close")
			return relayPeerResult{}
		}
	}
}

func relayTestItem() models.OutboundQueue {
	// The Bcc sentinel deliberately detects a second preparation pass. Production
	// callers are responsible for stripping it before submitting prepared bytes.
	return models.OutboundQueue{Sender: "sender@example.test", Recipient: "blind@example.test", RawMessage: "DKIM-Signature: v=1; b=synthetic\r\nFrom: sender@example.test\r\nBcc: fixture@example.test\r\nSubject: prepared\r\nX-Folded: first\r\n second\r\n\r\n.dot-stuffed\r\n..two dots\r\nbody\r\n"}
}

func relayAssertNoSubmission(t *testing.T, result relayPeerResult) {
	t.Helper()
	for _, command := range result.commands {
		for _, forbidden := range []string{"AUTH", "MAIL", "RCPT", "DATA"} {
			if strings.HasPrefix(command, forbidden) {
				t.Fatalf("unexpected submission command: %s", forbidden)
			}
		}
	}
	if result.data != "" {
		t.Fatal("unexpected message data")
	}
}

func TestRelaySuccess(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	for _, implicit := range []bool{false, true} {
		for _, auth := range []bool{false, true} {
			t.Run(fmt.Sprintf("implicit=%v/auth=%v", implicit, auth), func(t *testing.T) {
				settings, finish := relayTestPeer(t, cert, relayPeerOptions{implicit: implicit, requireAUTH: auth})
				settings.roots = roots
				if !auth {
					settings.username, settings.password = "", ""
				}
				item := relayTestItem()
				// Empty reverse-path and UTF-8 envelope/header/body are also preserved.
				if !auth {
					item.Sender = ""
				} else {
					item.Sender = "café@example.test"
					item.RawMessage = strings.Replace(item.RawMessage, "prepared", "préparé", 1)
					item.RawMessage += "café\r\n"
				}
				w := &Worker{hostname: "sender.test"}
				if err := w.deliverToRelay(context.Background(), settings, item); err != nil {
					t.Fatal(err)
				}
				result := finish()
				if result.data != item.RawMessage {
					t.Fatalf("prepared DATA changed: got %q, want %q", result.data, item.RawMessage)
				}
				if result.authBeforeTLS {
					t.Fatal("AUTH before TLS")
				}
				if auth && result.auth != "\x00"+relayTestUser+"\x00"+relayTestPassword {
					t.Fatal("incorrect AUTH payload")
				}
				if !auth && result.auth != "" {
					t.Fatal("unexpected AUTH")
				}
				commands := strings.Join(result.commands, "\n")
				if !strings.Contains(commands, "MAIL FROM:<"+item.Sender+"> BODY=8BITMIME SMTPUTF8") || !strings.Contains(commands, "RCPT TO:<"+item.Recipient+">") {
					t.Fatalf("envelope/capabilities changed: %s", commands)
				}
				wantEHLO := 2
				if implicit {
					wantEHLO = 1
				}
				if strings.Count(commands, "EHLO sender.test") != wantEHLO {
					t.Fatalf("EHLO capability refresh missing: %s", commands)
				}
			})
		}
	}
}

func TestRelayTLSRefusal(t *testing.T) {
	good, roots := relayTestCertificate(t, false)
	mismatch, mismatchRoots := relayTestCertificate(t, true)
	for _, tc := range []struct {
		name  string
		opts  relayPeerOptions
		cert  tls.Certificate
		roots *x509.CertPool
	}{
		{"missing STARTTLS", relayPeerOptions{noSTARTTLS: true, preTLSAUTH: true}, good, roots},
		{"STARTTLS rejected", relayPeerOptions{rejectAt: "STARTTLS", reply: "454 TLS unavailable"}, good, roots},
		{"broken handshake", relayPeerOptions{breakTLS: true}, good, roots},
		{"untrusted STARTTLS", relayPeerOptions{}, good, nil},
		{"hostname STARTTLS", relayPeerOptions{}, mismatch, mismatchRoots},
		{"untrusted implicit", relayPeerOptions{implicit: true}, good, nil},
		{"hostname implicit", relayPeerOptions{implicit: true}, mismatch, mismatchRoots},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, finish := relayTestPeer(t, tc.cert, tc.opts)
			settings.roots = tc.roots
			// Direct-path insecure testing configuration must never affect this adapter.
			w := &Worker{hostname: "sender.test", tlsInsecure: true}
			if err := w.deliverToRelay(context.Background(), settings, relayTestItem()); err == nil {
				t.Fatal("accepted insecure relay")
			}
			relayAssertNoSubmission(t, finish())
		})
	}
}

func TestRelayCapabilityRefusal(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	for _, tc := range []struct {
		name   string
		opts   relayPeerOptions
		mutate func(*models.OutboundQueue)
	}{
		{"missing AUTH", relayPeerOptions{noAUTH: true, preTLSAUTH: true}, func(*models.OutboundQueue) {}},
		{"unsupported AUTH", relayPeerOptions{authMechanisms: "LOGIN CRAM-MD5"}, func(*models.OutboundQueue) {}},
		{"8bit raw", relayPeerOptions{no8BIT: true}, func(item *models.OutboundQueue) { item.RawMessage += "café\r\n" }},
		{"8bit declared", relayPeerOptions{no8BIT: true}, func(item *models.OutboundQueue) { item.BodyType = "8BITMIME" }},
		{"UTF8 envelope", relayPeerOptions{noUTF8: true}, func(item *models.OutboundQueue) { item.Recipient = "café@example.test" }},
		{"UTF8 header", relayPeerOptions{noUTF8: true}, func(item *models.OutboundQueue) { item.RawMessage = "Subject: café\r\n\r\nbody\r\n" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, finish := relayTestPeer(t, cert, tc.opts)
			settings.roots = roots
			// No credentials needed for the message capability cases.
			if !tc.opts.noAUTH && tc.opts.authMechanisms == "" {
				settings.username, settings.password = "", ""
			}
			item := relayTestItem()
			tc.mutate(&item)
			w := &Worker{hostname: "sender.test"}
			if err := w.deliverToRelay(context.Background(), settings, item); err == nil {
				t.Fatal("missing capability accepted")
			}
			relayAssertNoSubmission(t, finish())
		})
	}
}

func TestRelayRejectionRedactsSecrets(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	encoded := base64.StdEncoding.EncodeToString([]byte("\x00" + relayTestUser + "\x00" + relayTestPassword))
	for _, stage := range []string{"greeting", "STARTTLS", "AUTH", "MAIL", "RCPT", "DATA", "DATA reply"} {
		for _, code := range []int{451, 535} {
			t.Run(stage+strconv.Itoa(code), func(t *testing.T) {
				reply := fmt.Sprintf("%d %d.7.8 %s %s %s", code, code/100, relayTestUser, relayTestPassword, encoded)
				settings, finish := relayTestPeer(t, cert, relayPeerOptions{rejectAt: stage, reply: reply})
				settings.roots = roots
				w := &Worker{hostname: "sender.test"}
				err := w.deliverToRelay(context.Background(), settings, relayTestItem())
				if err == nil {
					t.Fatal("rejection accepted")
				}
				var se *SMTPError
				if !errors.As(err, &se) || se.Code != code || se.Enhanced != fmt.Sprintf("%d.7.8", code/100) || se.RemoteMTA != settings.host {
					t.Fatalf("lost typed status: %v", err)
				}
				var raw *textproto.Error
				if errors.As(err, &raw) {
					t.Fatal("raw server reply escaped")
				}
				for _, secret := range []string{relayTestUser, relayTestPassword, encoded} {
					if strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%+v", se), secret) {
						t.Fatal("secret-bearing error escaped")
					}
				}
				if ClassifyFailure(err).Permanent() != (code >= 500) {
					t.Fatalf("classification lost: %v", err)
				}
				result := finish()
				if stage == "greeting" || stage == "STARTTLS" {
					relayAssertNoSubmission(t, result)
				}
				if stage == "AUTH" {
					for _, cmd := range result.commands {
						if strings.HasPrefix(cmd, "MAIL") || strings.HasPrefix(cmd, "DATA") {
							t.Fatal("submission after AUTH rejection")
						}
					}
				}
			})
		}
	}
}

func TestRelayAUTHRequired(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{requireAUTH: true})
	settings.roots = roots
	settings.username, settings.password = "", ""
	w := &Worker{hostname: "sender.test"}
	err := w.deliverToRelay(context.Background(), settings, relayTestItem())
	var se *SMTPError
	if !errors.As(err, &se) || se.Code != 530 {
		t.Fatalf("want AUTH-required status, got %v", err)
	}
	if finish().data != "" {
		t.Fatal("DATA after required AUTH missing")
	}
}

func TestRelayDeadlineAndCancellation(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	for _, stage := range []string{"greeting", "EHLO", "STARTTLS", "AUTH", "MAIL", "RCPT", "DATA"} {
		for _, cancelEarly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%v", stage, cancelEarly), func(t *testing.T) {
				settings, finish := relayTestPeer(t, cert, relayPeerOptions{silentAt: stage})
				settings.roots = roots
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				w := &Worker{hostname: "sender.test", sendDeadline: func(int64) time.Duration { return 100 * time.Millisecond }}
				want := context.DeadlineExceeded
				if cancelEarly {
					timer := time.AfterFunc(40*time.Millisecond, cancel)
					defer timer.Stop()
					want = context.Canceled
				}
				start := time.Now()
				err := w.deliverToRelay(ctx, settings, relayTestItem())
				if !errors.Is(err, want) {
					t.Fatalf("want %v, got %v", want, err)
				}
				if time.Since(start) > time.Second {
					t.Fatal("attempt exceeded budget")
				}
				finish()
			})
		}
	}
}

func TestRelayInvalidBeforeNetwork(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	base := relaySettings{host: "127.0.0.1", port: listener.Addr().(*net.TCPAddr).Port, mode: relaySTARTTLS}
	for _, tc := range []struct {
		name   string
		mutate func(*relaySettings, *models.OutboundQueue, *Worker)
	}{
		{"empty host", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.host = "" }},
		{"URL", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.host = "smtp://127.0.0.1" }},
		{"host port", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.host = "127.0.0.1:25" }},
		{"host injection", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.host = "a\r\nAUTH" }},
		{"bad label", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.host = "-bad.test" }},
		{"empty label", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.host = "a..test" }},
		{"zero port", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.port = 0 }},
		{"negative port", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.port = -1 }},
		{"large port", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.port = 65536 }},
		{"clear mode", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.mode = "plain" }},
		{"empty mode", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.mode = "" }},
		{"username only", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.username = relayTestUser }},
		{"password only", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.password = relayTestPassword }},
		{"credential NUL", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) {
			s.username = "a\x00b"
			s.password = "secret"
		}},
		{"password control", func(s *relaySettings, _ *models.OutboundQueue, _ *Worker) { s.username = "a"; s.password = "secret\n" }},
		{"sender CRLF", func(_ *relaySettings, i *models.OutboundQueue, _ *Worker) { i.Sender = "a\r\nDATA" }},
		{"recipient NUL", func(_ *relaySettings, i *models.OutboundQueue, _ *Worker) { i.Recipient = "a\x00b" }},
		{"recipient DEL", func(_ *relaySettings, i *models.OutboundQueue, _ *Worker) { i.Recipient = "a\x7fb" }},
		{"EHLO injection", func(_ *relaySettings, _ *models.OutboundQueue, w *Worker) { w.hostname = "a\r\nAUTH" }},
		{"bad UTF8", func(_ *relaySettings, i *models.OutboundQueue, _ *Worker) { i.Sender = "\xff@example.test" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, item, w := base, relayTestItem(), &Worker{hostname: "sender.test"}
			tc.mutate(&settings, &item, w)
			if err := w.deliverToRelay(context.Background(), settings, item); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	tcp := listener.(*net.TCPListener)
	_ = tcp.SetDeadline(time.Now().Add(10 * time.Millisecond))
	if conn, err := tcp.Accept(); err == nil {
		conn.Close()
		t.Fatal("invalid input reached network")
	}
}

func TestRelayMalformedAUTHChallengeRedacted(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{authChallenge: "!" + relayTestPassword})
	settings.roots = roots
	w := &Worker{hostname: "sender.test"}
	err := w.deliverToRelay(context.Background(), settings, relayTestItem())
	if err == nil || strings.Contains(err.Error(), relayTestPassword) {
		t.Fatalf("unsafe AUTH error: %v", err)
	}
	for _, cmd := range finish().commands {
		if strings.HasPrefix(cmd, "MAIL") || strings.HasPrefix(cmd, "DATA") {
			t.Fatal("submission after malformed AUTH challenge")
		}
	}
}

func TestRelayStalledTLSAndDATA(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	for _, tc := range []struct {
		name, stage string
		implicit    bool
	}{
		{"STARTTLS handshake", "TLS handshake", false},
		{"implicit handshake", "TLS handshake", true},
		{"DATA write", "DATA write", false},
		{"DATA reply", "DATA reply", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings, finish := relayTestPeer(t, cert, relayPeerOptions{implicit: tc.implicit, silentAt: tc.stage})
			settings.roots = roots
			item := relayTestItem()
			if tc.stage == "DATA write" {
				item.RawMessage += strings.Repeat("synthetic payload line\r\n", 400000)
			}
			var measuredSize int64
			w := &Worker{hostname: "sender.test", sendDeadline: func(size int64) time.Duration { measuredSize = size; return 100 * time.Millisecond }}
			started := time.Now()
			err := w.deliverToRelay(context.Background(), settings, item)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want deadline, got %v", err)
			}
			if time.Since(started) > time.Second {
				t.Fatal("stalled attempt exceeded budget")
			}
			if measuredSize != int64(len(item.RawMessage)) {
				t.Fatal("deadline budget did not receive prepared message size")
			}
			result := finish()
			if tc.stage == "TLS handshake" {
				relayAssertNoSubmission(t, result)
			}
		})
	}
}

func TestRelayCallerDeadlineWins(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{silentAt: "greeting"})
	settings.roots = roots
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	w := &Worker{hostname: "sender.test", sendDeadline: func(int64) time.Duration { return time.Hour }}
	started := time.Now()
	if err := w.deliverToRelay(ctx, settings, relayTestItem()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want caller deadline, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("caller deadline ignored")
	}
	finish()
}

func TestRelaySevenBitPeer(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	settings, finish := relayTestPeer(t, cert, relayPeerOptions{no8BIT: true, noUTF8: true, noAUTH: true})
	settings.roots = roots
	settings.username, settings.password = "", ""
	item := relayTestItem()
	w := &Worker{hostname: "sender.test"}
	if err := w.deliverToRelay(context.Background(), settings, item); err != nil {
		t.Fatal(err)
	}
	result := finish()
	if result.data != item.RawMessage {
		t.Fatal("7-bit DATA changed")
	}
	for _, cmd := range result.commands {
		if strings.HasPrefix(cmd, "MAIL") && cmd != "MAIL FROM:<"+item.Sender+">" {
			t.Fatalf("unexpected MAIL parameters: %s", cmd)
		}
	}
}
