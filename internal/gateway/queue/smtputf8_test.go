package queue

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

// Exercise both production SMTP paths over a real EHLO/STARTTLS/MAIL/RCPT/DATA
// conversation. Refusals must occur before MAIL, and successful delivery must
// preserve the raw headers/body, including a pre-existing DKIM signature.
func TestSMTPInternationalizedDelivery(t *testing.T) {
	cert, roots := relayTestCertificate(t, false)
	const asciiRaw = "DKIM-Signature: v=1; b=synthetic\r\nSubject: hello\r\n\r\nbody\r\n"
	const utf8Raw = "DKIM-Signature: v=1; b=synthetic\r\nSubject: café\r\n\r\nbody\r\n"
	for _, transport := range []string{"direct", "relay"} {
		for _, tc := range []struct {
			name                   string
			sender, recipient, raw string
			wantSender, wantRcpt   string
			opts                   relayPeerOptions
			status                 string
		}{
			{name: "ASCII on legacy peer", opts: relayPeerOptions{noUTF8: true, no8BIT: true}},
			{name: "IDN envelope on legacy peer", sender: "sender@faß.de", recipient: "user@例え.jp", wantSender: "sender@xn--fa-hia.de", wantRcpt: "user@xn--r8jz45g.jp", opts: relayPeerOptions{noUTF8: true, no8BIT: true}},
			{name: "empty reverse path", sender: "<empty>", opts: relayPeerOptions{noUTF8: true}},
			{name: "UTF8 sender refused", sender: "münchen@example.test", opts: relayPeerOptions{noUTF8: true}, status: "5.6.7"},
			{name: "UTF8 recipient refused", recipient: "用户@example.test", opts: relayPeerOptions{noUTF8: true}, status: "5.6.7"},
			{name: "UTF8 header refused", raw: utf8Raw, opts: relayPeerOptions{noUTF8: true}, status: "5.6.9"},
			{name: "UTF8 LF header refused", raw: "Subject: café\n\nbody\n", opts: relayPeerOptions{noUTF8: true}, status: "5.6.9"},
			{name: "UTF8 folded header refused", raw: "Subject: hello\r\n café\r\n\r\nbody\r\n", opts: relayPeerOptions{noUTF8: true}, status: "5.6.9"},
			{name: "UTF8 body only", raw: "Subject: hello\r\n\r\ncafé\r\n", opts: relayPeerOptions{noUTF8: true}},
			{name: "UTF8 LF body only", raw: "Subject: hello\n\ncafé\n", opts: relayPeerOptions{noUTF8: true}},
			{name: "encoded word on legacy peer", raw: "Subject: =?UTF-8?Q?caf=C3=A9?=\r\n\r\nbody\r\n", opts: relayPeerOptions{noUTF8: true, no8BIT: true}},
			{name: "UTF8 local and IDN", recipient: "用户@例え.jp", wantRcpt: "用户@xn--r8jz45g.jp"},
			{name: "UTF8 header accepted", raw: utf8Raw},
			{name: "UTF8 withdrawn after STARTTLS", recipient: "用户@example.test", opts: relayPeerOptions{utf8BeforeTLSOnly: true}, status: "5.6.7"},
			{name: "UTF8 requires 8BITMIME", recipient: "用户@example.test", opts: relayPeerOptions{no8BIT: true}, status: "5.6.3"},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				opts := tc.opts
				opts.noAUTH = true
				settings, finish := relayTestPeer(t, cert, opts)
				settings.roots, settings.username, settings.password = roots, "", ""
				item := models.OutboundQueue{Sender: "sender@example.test", Recipient: "user@example.test", RawMessage: asciiRaw}
				if tc.sender != "" {
					item.Sender = tc.sender
					if tc.sender == "<empty>" {
						item.Sender = ""
					}
				}
				if tc.recipient != "" {
					item.Recipient = tc.recipient
				}
				if tc.raw != "" {
					item.RawMessage = tc.raw
				}
				w := &Worker{hostname: "sender.test", tlsInsecure: true, allowPrivateDest: true, sendDeadline: defaultSendDeadline}
				var err error
				if transport == "direct" {
					err = w.deliverSMTP(net.JoinHostPort(settings.host, strconv.Itoa(settings.port)), settings.host, item, nil)
				} else {
					err = w.deliverToRelay(context.Background(), settings, item)
				}
				result := finish()
				if tc.status != "" {
					var se *SMTPError
					if !errors.As(err, &se) || se.Enhanced != tc.status || se.RemoteMTA != settings.host || !ClassifyFailure(err).Permanent() {
						t.Fatalf("delivery error = %v, want permanent %s from %s", err, tc.status, settings.host)
					}
					relayAssertNoSubmission(t, result)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				wantSender, wantRcpt := item.Sender, item.Recipient
				if tc.wantSender != "" {
					wantSender = tc.wantSender
				}
				if tc.wantRcpt != "" {
					wantRcpt = tc.wantRcpt
				}
				mailCommand := "MAIL FROM:<" + wantSender + ">"
				if !opts.no8BIT {
					mailCommand += " BODY=8BITMIME"
				}
				if !opts.noUTF8 {
					mailCommand += " SMTPUTF8"
				}
				commands := strings.Join(result.commands, "\n")
				if !strings.Contains(commands, mailCommand+"\n") || !strings.Contains(commands, "RCPT TO:<"+wantRcpt+">\n") {
					t.Fatalf("unexpected SMTP envelope: %s", commands)
				}
				wantData := strings.ReplaceAll(strings.ReplaceAll(item.RawMessage, "\r\n", "\n"), "\n", "\r\n")
				if result.data != wantData {
					t.Fatalf("message changed: got %q, want %q", result.data, wantData)
				}
			})
		}
	}
}
