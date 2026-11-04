// Package wireform is the form of a message FETCH sends (RFC 3501 §7.4.2): a
// host measures [Convert] when it stores a message, so [imap.Message.Size] is
// the octet count the client receives, without loading the message on every
// FETCH RFC822.SIZE or SEARCH LARGER.
package wireform

import "github.com/rest-mail/go-imap/pkg/imap/internal/message"

// Convert returns raw as FETCH sends it. Headers go out 7-bit, non-textual body
// data goes out transfer-encoded, and 8-bit textual data stays when its section
// names a charset. A message that is already 7-bit comes back byte for byte.
// utf8 is whether the session enabled UTF8=ACCEPT (RFC 6855): valid UTF-8 then
// stays in headers and in a textual body that names no charset.
func Convert(raw string, utf8 bool) string {
	return message.WireForm(raw, utf8)
}

// EncodeWords renders text as RFC 2047 encoded words: charset UTF-8 when it is
// valid UTF-8, and UNKNOWN-8BIT otherwise, so the octets survive and no charset
// is guessed.
func EncodeWords(text string) string {
	return message.EncodeWords(text)
}

// ParameterField renders a Content-Type or Content-Disposition field 7-bit, changing
// only what holds an 8-bit octet or couldn't be read once 7-bit, so a parameter in any
// charset or spelling keeps its text. ok is false when the value holds no readable
// type, or the type itself is 8-bit.
func ParameterField(name, value string) (field string, ok bool) {
	return message.ParameterField(name, value)
}
