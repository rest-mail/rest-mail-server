package smtp

import (
	"bytes"
	"unicode/utf8"

	gosmtp "github.com/rest-mail/go-smtp/pkg/smtp"
)

// go-smtp accepts non-ASCII envelope bytes even without the MAIL parameter.
// Reject them before mailbox lookup or authorization, without echoing an
// internationalized address in a reply (RFC 6531 sections 3.5 and 3.7.4).
func validateUTF8Address(address string, enabled bool, code int) error {
	if !utf8.ValidString(address) {
		return &gosmtp.SMTPError{
			Code: code, EnhancedCode: gosmtp.EnhancedCode{5, 1, 3},
			Message: "Invalid UTF-8 in envelope address",
		}
	}
	if !enabled && containsNonASCII([]byte(address)) {
		return &gosmtp.SMTPError{
			Code: code, EnhancedCode: gosmtp.EnhancedCode{5, 6, 7},
			Message: "Internationalized envelope address requires SMTPUTF8",
		}
	}
	return nil
}

// Validate only the top-level header block: body octets are governed by
// 8BITMIME, not SMTPUTF8. Leave the original bytes intact for DKIM verification.
func validateUTF8Headers(data []byte, enabled bool) error {
	headers := data
	for start := 0; start < len(data); {
		nl := bytes.IndexByte(data[start:], '\n')
		if nl < 0 {
			break
		}
		end := start + nl
		if end == start || (end == start+1 && data[start] == '\r') {
			headers = data[:start]
			break
		}
		start = end + 1
	}
	if !utf8.Valid(headers) {
		return &gosmtp.SMTPError{
			Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 6, 0},
			Message: "Invalid UTF-8 in message headers",
		}
	}
	if !enabled && containsNonASCII(headers) {
		return &gosmtp.SMTPError{
			Code: 554, EnhancedCode: gosmtp.EnhancedCode{5, 6, 9},
			Message: "Internationalized message headers require SMTPUTF8",
		}
	}
	return nil
}

func containsNonASCII(data []byte) bool {
	for _, b := range data {
		if b >= utf8.RuneSelf {
			return true
		}
	}
	return false
}
