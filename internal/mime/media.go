package mime

import (
	stdmime "mime"
	"strings"
	"unicode/utf8"

	gomessage "github.com/emersion/go-message"
	"github.com/rest-mail/go-imap/pkg/imap/wireform"
)

// parseContentType reads a Content-Type: the type and the parameters this package looks at, as
// go-imap reads them for BODYSTRUCTURE and its sections, so a filter is shown the parts a mail client
// is. A field that gives no usable type is failed, and is text/plain with no parameters (RFC 2045
// §5.2). An empty one is simply that. value may be a view of a buffer, and nothing it answers is.
func parseContentType(value string) (mediaType string, params map[string]string, failed bool) {
	if value == "" {
		return "text/plain", nil, false
	}
	mediaType, read, usable := wireform.ReadContentType(value)
	if !usable {
		return "text/plain", nil, true
	}
	return strings.Clone(mediaType), heldParameters(read), false
}

// parseDisposition reads a Content-Disposition as go-imap does for BODYSTRUCTURE, and when that has
// none, there is none. value may be a view of a buffer, and nothing it answers is.
func parseDisposition(value string) (disposition string, params map[string]string) {
	disposition, read, usable := wireform.ReadDisposition(value)
	if !usable {
		return "", nil
	}
	return strings.Clone(disposition), heldParameters(read)
}

// heldParameters is what's kept of parameters that may be views of a buffer. A boundary and a charset
// name are their octets (RFC 2046 §5.1.1), since delimiter lines match octets and nothing decodes
// those, and only a name is text.
func heldParameters(read map[string]string) map[string]string {
	if len(read) == 0 {
		return nil
	}
	params := make(map[string]string, len(read))
	if boundary, ok := read["boundary"]; ok {
		params["boundary"] = strings.Clone(boundary)
	}
	if charset, ok := read["charset"]; ok {
		params["charset"] = strings.Clone(charset)
	}
	if name, ok := read["name"]; ok {
		params["name"] = parameterText(name)
	}
	if filename, ok := read["filename"]; ok {
		params["filename"] = parameterText(filename)
	}
	return params
}

// parameterText is a parameter as text for a row and a filter alike: its RFC 2047 encoded-words
// decoded, a NUL as a space, as it reads in what's sent of a message (#1921), and octets that aren't
// UTF-8 as U+FFFD, since nothing says what charset they're in. A client is sent those octets in
// encoded words naming UNKNOWN-8BIT, which is the same reading in the one spelling it can take.
func parameterText(value string) string {
	if utf8.ValidString(value) {
		value = decodeWords(value)
	}
	if strings.IndexByte(value, 0) >= 0 {
		value = strings.ReplaceAll(value, "\x00", " ")
	}
	return strings.Clone(strings.ToValidUTF8(value, "\uFFFD"))
}

// decodeWords is a header value with its RFC 2047 encoded-words decoded, or the value when it can't
// be, as go-message reads one.
func decodeWords(value string) string {
	decoder := stdmime.WordDecoder{CharsetReader: gomessage.CharsetReader}
	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}
