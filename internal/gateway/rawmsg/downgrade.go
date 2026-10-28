package rawmsg

import rmail "github.com/restmail/restmail/internal/mail"

// Downgrade rewrites the top-level header block of a stored message so it
// carries only ASCII, for a POP3 or IMAP client that has not enabled UTF-8.
// The rules live in internal/mail, where the API also measures the downgraded
// size at delivery, so the size a gateway reports and the octets it sends come
// from the same code.
func Downgrade(raw string) string {
	return rmail.DowngradeHeaders(raw)
}
