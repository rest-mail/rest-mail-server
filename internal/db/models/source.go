package models

import (
	"bytes"
	"io"
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

// readableChunk is how much of a text NewReadableReader works on at a time.
const readableChunk = 32 << 10

// NewReadableReader answers what ReadableText answers for all that src holds, without holding it.
func NewReadableReader(src io.Reader) io.Reader {
	return &readableReader{src: src, in: make([]byte, readableChunk)}
}

type readableReader struct {
	src     io.Reader
	in      []byte
	pending int // octets of a character the last chunk cut, at the start of in
	out     []byte
	invalid bool // the last octet seen was invalid: ReadableText writes one U+FFFD for a run of them
	err     error
}

func (r *readableReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		n, err := r.src.Read(r.in[r.pending:])
		have := r.in[:r.pending+n]
		ready := have
		if err == nil {
			ready = have[:len(have)-cutRune(have)]
		}
		r.err = err
		r.out = r.appendReadable(r.out[:0], ready)
		r.pending = copy(r.in, have[len(ready):])
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

func (r *readableReader) appendReadable(out, text []byte) []byte {
	for len(text) > 0 {
		c, size := utf8.DecodeRune(text)
		switch {
		case c == utf8.RuneError && size == 1:
			if !r.invalid {
				out = utf8.AppendRune(out, utf8.RuneError)
			}
			r.invalid = true
		case c == 0:
			out = utf8.AppendRune(out, utf8.RuneError)
			r.invalid = false
		default:
			out = append(out, text[:size]...)
			r.invalid = false
		}
		text = text[size:]
	}
	return out
}

// cutRune is how many octets at the end of text begin a character the next chunk may finish.
func cutRune(text []byte) int {
	for i := len(text) - 1; i >= 0 && i >= len(text)-utf8.UTFMax; i-- {
		if utf8.RuneStart(text[i]) {
			if !utf8.FullRune(text[i:]) {
				return len(text) - i
			}
			return 0
		}
	}
	return 0
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
