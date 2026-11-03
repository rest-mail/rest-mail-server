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
// can compare: UTF-8 and US-ASCII natively, ISO-8859-1 by conversion.
var searchHeaderDecoder = &mime.WordDecoder{
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		if !isLatin1(charset) {
			return nil, fmt.Errorf("unsupported charset %q", charset)
		}
		b, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		return strings.NewReader(latin1ToUTF8(b)), nil
	},
}

// DecodeSearchHeader decodes the encoded words in a header value or block for
// SEARCH (RFC 3501 §6.4.4). Text that cannot be decoded is compared as stored.
func DecodeSearchHeader(s string) string {
	if !strings.Contains(s, "=?") {
		return s
	}
	d, err := searchHeaderDecoder.DecodeHeader(s)
	if err != nil {
		return s
	}
	return d
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
