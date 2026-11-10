package mime

import (
	"strings"
	"unicode/utf8"

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
	boundary, hasBoundary := read["boundary"]
	charset, hasCharset := read["charset"]
	name, hasName := read["name"]
	filename, hasFilename := read["filename"]
	// The map is this reading's and has room for these, and a part reads two of them. Its keys can be
	// views too, so none stays.
	clear(read)
	if hasBoundary {
		read["boundary"] = strings.Clone(boundary)
	}
	if hasCharset {
		read["charset"] = strings.Clone(charset)
	}
	if hasName {
		read["name"] = parameterText(name)
	}
	if hasFilename {
		read["filename"] = parameterText(filename)
	}
	return read
}

// parameterText is a parameter as text for a row and a filter alike: its RFC 2047 encoded-words
// decoded, a NUL as a space whether or not a word spells it, as it reads in what's sent of a message
// (#1921), and octets that aren't UTF-8 as U+FFFD, since nothing says what charset they're in. A client
// is sent those octets in encoded words naming UNKNOWN-8BIT, which is the same reading in the one
// spelling it can take.
func parameterText(value string) string {
	value = wireform.SpaceWordNUL(value)
	if utf8.ValidString(value) {
		value = decodeWords(value)
	}
	if strings.IndexByte(value, 0) >= 0 {
		value = strings.ReplaceAll(value, "\x00", " ")
	}
	return strings.Clone(strings.ToValidUTF8(value, "\uFFFD"))
}
