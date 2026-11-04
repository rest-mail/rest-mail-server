package rawmsg

import rmail "github.com/rest-mail/rest-mail-server/internal/mail"

// Downgrade rewrites the top-level header block of a stored message so it
// carries only ASCII, for a POP3 or IMAP client that has not enabled UTF-8.
// The rules live in internal/mail, where the API also measures the downgraded
// size at delivery, so the size a gateway reports and the octets it sends come
// from the same code.
func Downgrade(raw string) string {
	return rmail.DowngradeHeaders(raw)
}

// DowngradeForPOP3 is Downgrade plus a top-level 8-bit boundary in a spelling a client
// can match, because POP3 sends the body as stored. See rmail.DowngradeForPOP3.
func DowngradeForPOP3(raw string) string {
	return rmail.DowngradeForPOP3(raw)
}
