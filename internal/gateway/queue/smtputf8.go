package queue

import (
	"net/smtp"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
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

// smtpUTF8Status is the RFC 3463 status of a message that needs SMTPUTF8 and can't have it, or
// empty when neither its envelope nor its header section needs the extension. 8-bit in the body
// needs only 8BITMIME.
func smtpUTF8Status(sender, recipient, raw string) string {
	if containsNonASCII(sender) || containsNonASCII(recipient) {
		return "5.6.7"
	}
	headers, _, found := strings.Cut(raw, "\r\n\r\n")
	if !found {
		headers, _, _ = strings.Cut(raw, "\n\n")
	}
	if containsNonASCII(headers) {
		return "5.6.9"
	}
	return ""
}

// checkSMTPUTF8 runs after STARTTLS (which replaces the EHLO capabilities) and before MAIL. Never
// rewrite signed headers to attempt a downgrade: reject this hop and let the caller try another MX.
func checkSMTPUTF8(client *smtp.Client, sender, recipient, raw, host string) error {
	status := smtpUTF8Status(sender, recipient, raw)
	if status == "" {
		return nil
	}
	if ok, _ := client.Extension("SMTPUTF8"); ok {
		// SMTPUTF8 requires BODY=8BITMIME even when only the local part is internationalized
		// (RFC 6531 section 3.2).
		if ok, _ := client.Extension("8BITMIME"); !ok {
			return &SMTPError{Code: 550, Enhanced: "5.6.3", Message: "SMTPUTF8 next hop does not support required 8BITMIME", RemoteMTA: host, HopLacksExtension: true}
		}
		return nil
	}
	return &SMTPError{Code: 550, Enhanced: status, Message: "next hop does not support required SMTPUTF8", RemoteMTA: host, HopLacksExtension: true}
}

func containsNonASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}
