package message

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
)

// searchHeaderDecoder decodes RFC 2047 encoded words in the charsets SEARCH
// can compare: UTF-8 and US-ASCII natively, ISO-8859-1 by conversion. It reads one
// word at a time, see DecodeSearchHeader.
var searchHeaderDecoder = &mime.WordDecoder{
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		if !isLatin1(charset) {
			return nil, fmt.Errorf("unsupported charset %q", charset)
		}
		return latin1Reader(input)
	},
}

// DecodeSearchHeader decodes the encoded words in a header value or block for
// SEARCH (RFC 3501 §6.4.4), one word at a time, because mime.WordDecoder.DecodeHeader
// gives up on everything when it meets a charset with no reader, or a "=?" that opens
// no word. A word that can't be decoded is compared as stored, and the words around
// it still decode. White space between two decoded words isn't text (RFC 2047 §6.2);
// beside a word left as stored, it stays.
func DecodeSearchHeader(s string) string {
	if !strings.Contains(s, "=?") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	written, at, lineEnd := 0, 0, 0
	afterDecoded := false
	for {
		i := strings.Index(s[at:], "=?")
		if i < 0 {
			break
		}
		start := at + i
		if start >= lineEnd {
			lineEnd = len(s)
			if j := strings.IndexAny(s[start:], "\r\n"); j >= 0 {
				lineEnd = start + j
			}
		}
		size, skip := encodedWordAt(s[start:lineEnd])
		if size == 0 {
			at = start + skip
			continue
		}
		word := s[start : start+size]
		decoded, err := searchHeaderDecoder.DecodeHeader(word)
		if err != nil {
			decoded = word
		}
		isDecoded := decoded != word
		if !isDecoded || !afterDecoded || strings.Trim(s[written:start], " \t\r\n") != "" {
			b.WriteString(s[written:start])
		}
		b.WriteString(decoded)
		written = start + size
		at = written
		afterDecoded = isDecoded
	}
	b.WriteString(s[written:])
	return b.String()
}

// encodedWordAt finds the encoded word that line starts with, line beginning "=?" and
// ending where its line does: a word never spans lines (RFC 2047 §2). It returns the
// word's size, or 0 and how far on the next one can start, so a line full of "=?" is
// read once. It reads a word in a charset SEARCH decodes as loosely as
// mime.WordDecoder, which then decides whether the encoding and text are valid. A
// word it will leave as stored ends at white space, so it can't swallow a real word
// later on the line.
func encodedWordAt(line string) (size, skip int) {
	q := strings.IndexByte(line[2:], '?')
	if q < 0 {
		return 0, len(line)
	}
	charset := line[2 : 2+q]
	at := 2 + q + 1
	if at+1 >= len(line) || line[at+1] != '?' {
		return 0, 2
	}
	at += 2
	if decodableCharset(charset) {
		end := strings.Index(line[at:], "?=")
		if end < 0 {
			return 0, len(line)
		}
		return at + end + 2, 0
	}
	for i := at; i < len(line); i++ {
		switch {
		case line[i] == ' ' || line[i] == '\t':
			return 0, i
		case line[i] == '?' && i+1 < len(line) && line[i+1] == '=':
			return i + 2, 0
		}
	}
	return 0, len(line)
}

// decodableCharset reports whether go-imap has a reader for charset.
func decodableCharset(charset string) bool {
	return strings.EqualFold(charset, "UTF-8") || strings.EqualFold(charset, "US-ASCII") || isLatin1(charset)
}

// SearchMessageText is the decoded text of a whole message for SEARCH TEXT:
// its header block with encoded words decoded, then its decoded body.
func SearchMessageText(raw string) string {
	return DecodeSearchHeader(HeaderSection(raw)) + SearchBody(raw)
}

// SearchBody is the decoded body of a whole message for SEARCH BODY.
func SearchBody(raw string) string {
	return searchBodyText(messageNode(raw, 0))
}

// searchBodyText decodes n's body for SEARCH BODY (RFC 3501 §6.4.4): every
// leaf has its Content-Transfer-Encoding removed and its charset converted to
// UTF-8, and an encapsulated MESSAGE/RFC822 contributes its decoded header and
// body, whether it is a part or the body of a message/rfc822 message. Parts are separated by CRLF so a match cannot span two parts. An
// entity at maxMIMEDepth is an opaque leaf, as in BODYSTRUCTURE.
func searchBodyText(n mimeNode) string {
	mt, params := n.mediaType()
	if mt == "message/rfc822" {
		inner := messageNode(n.body, n.depth+1)
		return DecodeSearchHeader(inner.header) + searchBodyText(inner)
	}
	if strings.HasPrefix(mt, "multipart/") && params["boundary"] != "" {
		var b strings.Builder
		for k := uint32(1); ; k++ {
			part, ok := n.child(k)
			if !ok {
				break
			}
			b.WriteString(searchBodyText(part))
			b.WriteString("\r\n")
		}
		return b.String()
	}
	return decodeSearchLeaf(n.header, n.body, params["charset"])
}

// decodeSearchLeaf removes a base64 or quoted-printable transfer encoding and
// converts ISO-8859-1 to UTF-8. A malformed encoding or a charset SEARCH
// cannot convert leaves the octets as stored.
func decodeSearchLeaf(header, body, charset string) string {
	cte := ""
	if v := HeaderFieldValues(header, "Content-Transfer-Encoding"); len(v) > 0 {
		cte = transferEncoding(strings.TrimSpace(v[0]))
	}
	decoded := body
	switch cte {
	case "BASE64":
		compact := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, body)
		if b, err := base64.StdEncoding.DecodeString(compact); err == nil {
			decoded = string(b)
		}
	case "QUOTED-PRINTABLE":
		if b, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body))); err == nil {
			decoded = string(b)
		}
	}
	if isLatin1(charset) {
		return latin1ToUTF8([]byte(decoded))
	}
	return decoded
}

func isLatin1(charset string) bool {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "iso8859-1", "latin1", "l1":
		return true
	}
	return false
}

// latin1Reader is input converted from ISO-8859-1 to UTF-8.
func latin1Reader(input io.Reader) (io.Reader, error) {
	b, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(latin1ToUTF8(b)), nil
}

func latin1ToUTF8(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// SearchMailbox is an envelope address as SEARCH FROM compares it: the decoded
// display name and the address, written "Name <address>", or the address alone
// when there is no name.
func SearchMailbox(a imap.Address) string {
	name := strings.TrimSpace(DecodeSearchHeader(a.Name))
	if name == "" {
		return a.Email
	}
	return name + " <" + a.Email + ">"
}
