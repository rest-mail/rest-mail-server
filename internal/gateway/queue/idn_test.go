package queue

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"golang.org/x/net/dns/dnsmessage"
)

func TestDeliverIDNMXLookup(t *testing.T) {
	// Return a null MX so deliver stops before any database access or SMTP
	// connection. The actual net.LookupMX query must use the ASCII domain.
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	questions := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := server.ReadFrom(buf)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(buf[:n]); err != nil || len(query.Questions) != 1 {
				return
			}
			question := query.Questions[0]
			select {
			case questions <- question.Name.String():
			default:
			}
			response := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true},
				Questions: query.Questions,
				Answers: []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeMX, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.MXResource{MX: dnsmessage.MustNewName(".")},
				}},
			}
			wire, err := response.Pack()
			if err != nil {
				return
			}
			if _, err := server.WriteTo(wire, addr); err != nil {
				return
			}
		}
	}()
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "udp", server.LocalAddr().String())
		},
	}
	t.Cleanup(func() { net.DefaultResolver = previous })

	item := models.OutboundQueue{Recipient: "user@例え.jp", Domain: "例え.jp"}
	err = NewWorker(nil, "sender.test", 1, time.Second).deliver(item)
	var se *SMTPError
	if !errors.As(err, &se) || se.Enhanced != "5.1.10" {
		t.Fatalf("delivery = %v, want null MX refusal", err)
	}
	select {
	case name := <-questions:
		if name != "xn--r8jz45g.jp." {
			t.Fatalf("DNS queried %q, want ASCII domain", name)
		}
	case <-time.After(time.Second):
		t.Fatal("no MX query received")
	}
	if item.Domain != "例え.jp" || item.Recipient != "user@例え.jp" {
		t.Fatal("stored domain or recipient changed")
	}
}

func TestInvalidSMTPEnvelopeBeforeNetwork(t *testing.T) {
	for _, recipient := range []string{"", "\xff@example.test", "user@bad_.test", "user\r\n@example.test", "user@"} {
		t.Run(recipient, func(t *testing.T) {
			item := models.OutboundQueue{Sender: "sender@example.test", Recipient: recipient, Domain: "example.test"}
			w := NewWorker(nil, "sender.test", 1, time.Second)
			// nil DB and an unusable destination make any attempt to reach
			// routing/storage/network a failure instead of a false positive.
			for _, deliver := range []func() error{
				func() error { return w.deliver(item) },
				func() error { return w.deliverSMTP("invalid", "invalid", item, nil) },
				func() error {
					return w.deliverToRelay(context.Background(), relaySettings{host: "example.test", port: 25, mode: relaySTARTTLS}, item)
				},
			} {
				err := deliver()
				var se *SMTPError
				if !errors.As(err, &se) || se.Enhanced != "5.1.3" || !ClassifyFailure(err).Permanent() {
					t.Fatalf("invalid envelope = %v, want permanent 5.1.3", err)
				}
			}
		})
	}
}
