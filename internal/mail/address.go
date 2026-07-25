package mail

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

// ContainsControlChar reports whether s contains an ASCII control character
// (any byte < 0x20 — including CR and LF — or DEL 0x7f). Such a byte must never
// appear in an address that is written into a message header or an SMTP envelope
// command: a CR/LF would terminate the line and let an attacker inject a forged
// header or a second SMTP command (e.g. an extra RCPT TO). It is the defense the
// outbound SMTP worker applies to MAIL FROM / RCPT TO arguments.
func ContainsControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// SanitizeHeaderValue neutralizes a value before it is interpolated into a mail
// header line or a structured DSN field. Every ASCII control character (any byte
// < 0x20 — CR and LF included — and DEL 0x7f) is replaced with a space, then
// runs of whitespace are collapsed to a single space and the result is trimmed.
//
// It is the defense applied to REMOTE-controlled text (a peer MX's multiline
// reply) and to envelope values (a recipient address that may carry control
// bytes on a poisoned queue row) before they are placed into a mailer-daemon DSN.
// Without it a CR/LF in that text would inject forged headers or fake
// message/delivery-status fields into a trusted bounce that bypasses inbound
// filters. Collapsing (rather than merely deleting) keeps a multiline remote
// reply readable as a single line.
func SanitizeHeaderValue(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ValidateAddress rejects an address that cannot be safely written into a
// message header or an SMTP envelope command. It rejects the empty string, any
// address containing a control character (defeating CR/LF header- and
// SMTP-command injection), and any address that net/mail cannot parse as a
// single RFC 5322 address. It is the validation applied at the API boundary to
// every recipient / Cc / Bcc / From / organizer address before the address is
// serialized into headers or queued for delivery.
func ValidateAddress(addr string) error {
	if strings.TrimSpace(addr) == "" {
		return errors.New("empty address")
	}
	if ContainsControlChar(addr) {
		return errors.New("address contains a control character")
	}
	if _, err := mail.ParseAddress(addr); err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	return nil
}
