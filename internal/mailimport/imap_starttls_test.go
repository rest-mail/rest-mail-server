package mailimport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	apitypes "github.com/restmail/restmail/internal/api/types"
)

func TestIMAPStartTLSPreservesReadOnlySource(t *testing.T) {
	raw := "Message-ID: <starttls@example.test>\r\nSubject: original\r\n\r\n\x00\xffbody\r\n"
	f := &imapSourceFixture{startTLS: true, raw: raw, flags: `\Seen \Answered \Flagged \Draft \Recent`, count: 1}
	options := newIMAPFixture(t, f)
	var messages []apitypes.ImportMessageBody
	err := IMAP(context.Background(), options, 1024, func(_ string, body apitypes.ImportMessageBody) error {
		messages = append(messages, body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Folder != "Archive/2020" || messages[1].Folder != "INBOX" {
		t.Fatalf("folder mapping: %+v", messages)
	}
	for _, message := range messages {
		_, offset := message.InternalDate.Zone()
		if !bytes.Equal(message.RawMessage, []byte(raw)) || offset != 7200 || message.InternalDate.Day() != 25 || !reflect.DeepEqual(message.Flags, []string{`\Seen`, `\Answered`, `\Flagged`, `\Draft`}) {
			t.Fatal("source octets, flags or INTERNALDATE changed")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	upgrade, login := -1, -1
	for i, command := range f.commands {
		switch command {
		case "STARTTLS":
			upgrade = i
		case "LOGIN":
			login = i
		case "SELECT", "STORE", "CLOSE", "EXPUNGE", "APPEND":
			t.Fatalf("source mutation: %s", command)
		}
	}
	if upgrade < 0 || login <= upgrade {
		t.Fatalf("authentication preceded required upgrade: %v", f.commands)
	}
}

func TestIMAPStartTLSRefusesUnsafeUpgrade(t *testing.T) {
	for _, kind := range []string{"refusal", "untrusted issuer", "wrong hostname", "old TLS", "verification disabled", "PREAUTH"} {
		t.Run(kind, func(t *testing.T) {
			f := &imapSourceFixture{startTLS: true, refuseTLS: kind == "refusal", preauth: kind == "PREAUTH"}
			options := newIMAPFixture(t, f)
			switch kind {
			case "untrusted issuer":
				options.TLSConfig.RootCAs = x509.NewCertPool()
			case "wrong hostname":
				_, port, err := net.SplitHostPort(options.Address)
				if err != nil {
					t.Fatal(err)
				}
				options.Address = net.JoinHostPort("localhost", port)
			case "old TLS":
				options.TLSConfig.MaxVersion = tls.VersionTLS11
			case "verification disabled":
				options.TLSConfig.InsecureSkipVerify = true
			}
			called := false
			err := IMAP(context.Background(), options, 1024, func(string, apitypes.ImportMessageBody) error { called = true; return nil })
			if err == nil || called || strings.Contains(err.Error(), options.Password) {
				t.Fatalf("unsafe source accepted or leaked details: called=%v error=%v", called, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, command := range f.commands {
				if command == "LOGIN" || command == "AUTHENTICATE" || command == "LIST" {
					t.Fatalf("continued after unsafe upgrade: %v", f.commands)
				}
			}
		})
	}
}

func TestIMAPStartTLSCancellationDuringUpgrade(t *testing.T) {
	for _, phase := range []string{"command response", "TLS handshake"} {
		t.Run(phase, func(t *testing.T) {
			seen := make(chan struct{})
			f := &imapSourceFixture{startTLS: true}
			if phase == "command response" {
				f.stallTLS, f.startTLSSeen = true, seen
			} else {
				f.stallHandshake, f.handshakeSeen = true, seen
			}
			options := newIMAPFixture(t, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- IMAP(ctx, options, 1024, func(string, apitypes.ImportMessageBody) error { return errors.New("unexpected destination visit") })
			}()
			select {
			case <-seen:
			case <-time.After(2 * time.Second):
				t.Fatal("source did not reach the pending upgrade phase")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not close the pending upgrade")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, command := range f.commands {
				if command == "LOGIN" || command == "AUTHENTICATE" {
					t.Fatal("credentials sent while upgrade was pending")
				}
			}

		})
	}
}
func TestIMAPStartTLSVisitorFailure(t *testing.T) {
	f := &imapSourceFixture{startTLS: true, raw: "Message-ID: <abort@example.test>\r\n\r\nbody", count: 1}
	options := newIMAPFixture(t, f)
	failure := errors.New("destination refused")
	done := make(chan error, 1)
	go func() {
		done <- IMAP(context.Background(), options, 1024, func(string, apitypes.ImportMessageBody) error { return failure })
	}()
	select {
	case err := <-done:
		if !errors.Is(err, failure) {
			t.Fatalf("visitor error changed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("visitor failure blocked on upgraded literal drain")
	}
}
