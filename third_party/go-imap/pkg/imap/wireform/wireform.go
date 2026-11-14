// Package wireform is the form of a message FETCH sends (RFC 3501 §7.4.2): a
// host measures [Convert] when it stores a message, so [imap.Message.Size] is
// the octet count the client receives, without loading the message on every
// FETCH RFC822.SIZE or SEARCH LARGER.
package wireform

import (
	"io"

	"github.com/rest-mail/go-imap/pkg/imap/internal/message"
)

// Convert returns raw as FETCH sends it. Headers go out 7-bit, non-textual body
// data goes out transfer-encoded, and 8-bit textual data stays when its section
// names a charset. A message that is already 7-bit comes back byte for byte.
// utf8 is whether the session enabled UTF8=ACCEPT (RFC 6855): valid UTF-8 then
// stays in headers and in a textual body that names no charset.
func Convert(raw string, utf8 bool) string {
	return message.WireForm(raw, utf8)
}

// Size is len(Convert(raw, utf8)): the same conversion, counted rather than built, so
// measuring a message at store time holds nothing that grows with its data.
func Size(raw string, utf8 bool) int {
	return message.WireFormSize(raw, utf8)
}

// JoinedSize is Size(block+body, utf8) without joining them, for a host that rewrites
// the header block of a message and keeps its stored body.
func JoinedSize(block, body string, utf8 bool) int {
	return message.WireFormJoinedSize(block, body, utf8)
}

// EncodeWords renders text as RFC 2047 encoded words: charset UTF-8 when it is
// valid UTF-8, and UNKNOWN-8BIT otherwise, so the octets survive and no charset
// is guessed.
func EncodeWords(text string) string {
	return message.EncodeWords(text)
}

// WriteEncodedWords is [EncodeWords] into w, holding one word at a time.
func WriteEncodedWords(w interface {
	io.Writer
	io.StringWriter
}, text string) {
	message.WriteEncodedWords(w, text)
}

// ParameterField renders a Content-Type or Content-Disposition field 7-bit, changing
// only what holds an 8-bit octet, so a parameter in any charset keeps its text. ok is
// false when the value does not parse, or the type itself is 8-bit.
func ParameterField(name, value string) (field string, ok bool) {
	return message.ParameterField(name, value)
}
