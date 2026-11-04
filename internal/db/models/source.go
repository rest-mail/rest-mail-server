package models

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// SourceFitsText reports whether a PostgreSQL text column can hold these octets: valid UTF-8
// with no NUL. Mail is often neither, and the source of a message must keep every octet.
func SourceFitsText(source string) bool {
	return utf8.ValidString(source) && strings.IndexByte(source, 0) < 0
}

// SourceColumns is a raw message as its two columns hold it: raw_message when text can represent
// it, otherwise raw_octets, so a row holds the exact octets either way.
func SourceColumns(source string) (text string, octets []byte) {
	if SourceFitsText(source) {
		return source, nil
	}
	return "", []byte(source)
}

// ReadableText is a value decoded from a message as a text column can hold it. These columns
// are for reading and searching and may lose what they can't show: the source keeps the octets.
func ReadableText(value string) string {
	if SourceFitsText(value) {
		return value
	}
	return strings.ToValidUTF8(strings.ReplaceAll(value, "\x00", "\uFFFD"), "\uFFFD")
}

// readableJSON replaces each escaped NUL in a JSON document, which a jsonb column refuses
// (RFC 8259 allows it). It is never written unescaped, and json.Marshal leaves no invalid UTF-8.
func readableJSON(document JSONB) JSONB {
	const escaped = `\u0000`
	if !bytes.Contains(document, []byte(escaped)) {
		return document
	}
	out := make(JSONB, 0, len(document))
	for i := 0; i < len(document); i++ {
		switch {
		case document[i] != '\\' || i+1 == len(document):
			out = append(out, document[i])
		case bytes.HasPrefix(document[i:], []byte(escaped)):
			out = append(out, `\ufffd`...)
			i += len(escaped) - 1
		default:
			// Any other escape, kept whole so an escaped backslash can't start one.
			out = append(out, document[i], document[i+1])
			i++
		}
	}
	return out
}
