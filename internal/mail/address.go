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
