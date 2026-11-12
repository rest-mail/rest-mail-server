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
// stays in headers and in a textual body that names no charset. A NUL in a header
// or in text comes back as a space, one octet for one; one in data is encoded.
func Convert(raw string, utf8 bool) string {
	return message.WireForm(raw, utf8)
}

// SpaceNUL returns raw with a space for each NUL in a header or in text, one octet for
// one. Where a NUL is data, in anything not text or in text whose charset has NUL
// octets, the entity stays as it is. This is for a protocol that sends a message as
// stored, with no BINARY to fetch the exact octets and no conversion to encode data.
func SpaceNUL(raw string) []byte {
	return message.SpaceNUL(raw)
}

// SpaceWordNUL is text with a space for each NUL one of its encoded words spells, as
// ENVELOPE and BODYSTRUCTURE send it, so a host reading a Subject or a part's name reads
// what a client decodes. See [Convert] for a NUL anywhere else.
func SpaceWordNUL(text string) string {
	return message.SpaceWordNUL(text)
}

// Size is len(Convert(raw, utf8)): the same conversion, counted rather than built, so
// measuring a message at store time holds nothing that grows with its data.
func Size(raw string, utf8 bool) int {
	return message.WireFormSize(raw, utf8)
}

// Sizes is Size with and without UTF-8, from one reading of what the two share.
func Sizes(raw string) (sevenBit, utf8 int) {
	return message.WireFormSizes(raw)
}

// JoinedSize is Size(block+body, utf8) without joining them, for a host that rewrites
// the header block of a message and keeps its stored body.
func JoinedSize(block, body string, utf8 bool) int {
	return message.WireFormJoinedSize(block, body, utf8)
}

// SevenBitBoundary is raw for a host that downgrades only the top-level header and
// sends the body as stored, as POP3 does. A top-level multipart's 8-bit boundary takes
// the 7-bit spelling [Convert] gives it, in Content-Type and on its delimiter lines, so
// a client can split the parts without decoding anything. Any other message comes back
// as it is. It doesn't make the rest of the header 7-bit.
func SevenBitBoundary(raw string) string {
	return message.SevenBitBoundary(raw)
}

// WithoutRepeats is a Content-Type or Content-Disposition value with only the first
// parameter of each name, which is how BODYSTRUCTURE reads a repeated name. repeated is
// false when no name repeats, or a later value doesn't read, which leaves the field to
// RFC 2045 §5.2's default. It says what a host reading such a field has to answer.
func WithoutRepeats(value string) (first string, repeated bool) {
	return message.WithoutRepeats(value)
}

// ReadContentType reads a Content-Type as BODYSTRUCTURE and sections read it, so a host can't
// be told of different parts from those a client is. It keeps only boundary, charset, filename and
// name, and holds nothing for any other parameter, however long the field. usable is false when the
// field gives the entity no type, so RFC 2045 §5.2's default stands: it can't be read, it names no
// subtype, or it's a multipart with no boundary. Its answers may be views of value.
func ReadContentType(value string) (mediaType string, params map[string]string, usable bool) {
	return message.ReadContentType(value)
}

// ReadDisposition is [ReadContentType] of a Content-Disposition. usable is false where BODYSTRUCTURE
// reports none.
func ReadDisposition(value string) (disposition string, params map[string]string, usable bool) {
	return message.ReadDisposition(value)
}

// BodyStructure is the BODYSTRUCTURE of a stored message as a session reading UTF-8 is sent it, so
// a host can prove what it reads of a message against what a client is told.
func BodyStructure(raw string) string {
	return message.BuildBodyStructure(raw, true, true)
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
// only what holds an 8-bit octet or couldn't be read once 7-bit, so a parameter in any
// charset or spelling keeps its text. ok is false when the value holds no readable
// type, or the type itself is 8-bit.
func ParameterField(name, value string) (field string, ok bool) {
	return message.ParameterField(name, value)
}

// WriteParameterField is [ParameterField] into w, holding one parameter at a time. It
// writes nothing when ok would be false.
func WriteParameterField(w interface {
	io.Writer
	io.StringWriter
}, name, value string) (ok bool) {
	return message.WriteParameterField(w, name, value)
}

// WriteFoldedParameterField is [WriteParameterField] of value with its line breaks taken out, when that
// doesn't change how any piece of it reads, so nothing is built to take them out. read is false when
// they'd have to be, and nothing is written.
func WriteFoldedParameterField(w interface {
	io.Writer
	io.StringWriter
}, name, value string) (ok, read bool) {
	return message.WriteFoldedParameterField(w, name, value)
}

// FieldScratch is memory for what reading one field builds and then drops, which a host that reads a
// long field more than once uses again. See [TakeFieldScratch].
type FieldScratch = message.FieldScratch

// TakeFieldScratch is an empty [FieldScratch] with room for size without growing. What its String and
// Unfolded answer is good only until its Release.
func TakeFieldScratch(size int) *FieldScratch { return message.TakeFieldScratch(size) }
