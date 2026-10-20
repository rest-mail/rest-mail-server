package queue

import (
	"net/smtp"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
)

func smtpEnvelope(item models.OutboundQueue) (sender, recipient string, err error) {
	sender, err = rmail.SMTPAddressASCII(item.Sender)
	if err == nil {
		recipient, err = rmail.SMTPAddressASCII(item.Recipient)
	}
	if err != nil || recipient == "" {
		return "", "", &SMTPError{Code: 550, Enhanced: "5.1.3", Message: "invalid SMTP envelope address"}
	}
	return sender, recipient, nil
}

// checkSMTPUTF8 runs after STARTTLS (which replaces the EHLO capabilities) and
// before MAIL. net/smtp adds SMTPUTF8 when advertised, but does not reject
// internationalized mail when it is absent. Never rewrite signed headers to
// attempt a downgrade: reject this hop and let the caller try another MX.
func checkSMTPUTF8(client *smtp.Client, sender, recipient, raw, host string) error {
	status := ""
	if containsNonASCII(sender) || containsNonASCII(recipient) {
		status = "5.6.7"
	} else {
		headers, _, found := strings.Cut(raw, "\r\n\r\n")
		if !found {
			headers, _, _ = strings.Cut(raw, "\n\n")
		}
		if containsNonASCII(headers) {
			status = "5.6.9"
		}
	}
	if status != "" {
		if ok, _ := client.Extension("SMTPUTF8"); ok {
			// SMTPUTF8 requires BODY=8BITMIME even when only the local part
			// is internationalized (RFC 6531 section 3.2). net/smtp adds
			// BODY=8BITMIME only if the peer advertises that extension.
			if ok, _ := client.Extension("8BITMIME"); !ok {
				return &SMTPError{Code: 550, Enhanced: "5.6.3", Message: "SMTPUTF8 next hop does not support required 8BITMIME", RemoteMTA: host}
			}
			return nil
		}
		return &SMTPError{Code: 550, Enhanced: status, Message: "next hop does not support required SMTPUTF8", RemoteMTA: host}
	}
	return nil
}

func containsNonASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}
