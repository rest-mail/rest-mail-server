package queue

import (
	"errors"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
)

// TestDeliver_RejectsControlCharAddress proves the outbound worker refuses a
// queue row whose envelope address carries CR/LF (or any control character)
// BEFORE it reaches net/smtp's client.Mail / client.Rcpt, which do not sanitize
// their arguments. Without this a recipient such as
// "victim@real.invalid>\r\nRCPT TO:<attacker@evil.com" would inject a second RCPT
// command. deliver() returns immediately (no MX lookup, no dial), so the test
// needs no network and no database.
func TestDeliver_RejectsControlCharAddress(t *testing.T) {
	w := NewWorker(nil, "mx.test", 1, time.Second)

	cases := []struct {
		name string
		item models.OutboundQueue
	}{
		{
			name: "CRLF-injecting recipient",
			item: models.OutboundQueue{
				Sender:     "sender@mx.test",
				Recipient:  "victim@real.invalid>\r\nRCPT TO:<attacker@evil.com",
				Domain:     "real.invalid",
				RawMessage: "From: sender@mx.test\r\n\r\nbody\r\n",
			},
		},
		{
			name: "CRLF-injecting sender",
			item: models.OutboundQueue{
				Sender:     "sender@mx.test>\r\nDATA",
				Recipient:  "user@real.invalid",
				Domain:     "real.invalid",
				RawMessage: "From: sender@mx.test\r\n\r\nbody\r\n",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := w.deliver(tc.item)
			if err == nil {
				t.Fatal("deliver accepted a control-character address; SMTP command injection possible")
			}
			var se *SMTPError
			if !errors.As(err, &se) {
				t.Fatalf("deliver returned %T (%v), want a permanent *SMTPError", err, err)
			}
			if !se.IsPermanent() {
				t.Fatalf("control-character address should fail permanently, got code %d", se.Code)
			}
		})
	}
}
