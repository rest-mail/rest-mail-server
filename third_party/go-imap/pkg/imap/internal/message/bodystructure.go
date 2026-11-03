package message

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"sort"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// BuildBodyStructure parses a raw RFC 5322 message and returns its IMAP body
// structure as a correctly-formed parenthesized structure (RFC 3501 §7.4.2).
// With extension=false it produces the BODY (non-extensible) form; with
// extension=true it produces BODYSTRUCTURE, appending the extension fields
// (MD5/disposition/language/location for single parts; parameters/disposition/
// language/location for multiparts) as NIL where the Message model has no source.
// The whole message body is always described — never a NIL or empty structure —
// so the response is well-formed even for unusual or unparseable input.
func BuildBodyStructure(raw string, extension bool) string {
	return messageStructure(raw, extension, 0)
}

// messageStructure describes the RFC 5322 message raw found at MIME nesting
// depth (see maxMIMEDepth).
func messageStructure(raw string, extension bool, depth int) string {
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		// Unparseable header block: describe the entire input as one text/plain
		// part so the structure is still well-formed.
		return singlePartStructure("text", "plain", nil, textproto.MIMEHeader{}, []byte(raw), extension, depth)
	}
	body, _ := io.ReadAll(msg.Body)
	return partStructure(textproto.MIMEHeader(msg.Header), body, extension, "text/plain", depth)
}

// parseContentType adapts mail's RFC 822 lexical rules to mime.ParseMediaType,
// which does not accept comments or whitespace around the type/subtype slash.
func parseContentType(value string) (string, map[string]string, error) {
	uncommented, err := uncommentMIME(value)
	if err != nil {
		return "", nil, err
	}
	base, parameters, hasParameters := strings.Cut(uncommented, ";")
	if typ, subtype, ok := strings.Cut(base, "/"); ok {
		base = strings.TrimSpace(typ) + "/" + strings.TrimSpace(subtype)
	}
	if hasParameters {
		base += ";" + parameters
	}
	return mime.ParseMediaType(base)
}

// uncommentMIME replaces each RFC 822 §3.4.3 comment in a MIME header value
// (RFC 2045 §5.1, RFC 2183 §2) with a space for mime.ParseMediaType. Comments
// separate tokens; deleting them must not join an invalid split token. Quoted
// parameter values (including parentheses and quoted-pairs) stay intact.
// Unbalanced comments and unterminated quoted strings are errors.
func uncommentMIME(value string) (string, error) {
	var b strings.Builder
	depth := 0
	quoted := false
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case depth > 0:
			switch c {
			case '\\':
				i++
			case '(':
				depth++
			case ')':
				depth--
			}
		case quoted:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(value) {
				i++
				b.WriteByte(value[i])
			} else if c == '"' {
				quoted = false
			}
		case c == '(':
			depth = 1
			b.WriteByte(' ')
		case c == ')':
			return "", mime.ErrInvalidMediaParameter
		default:
			b.WriteByte(c)
			quoted = c == '"'
		}
	}
	if depth != 0 || quoted {
		return "", mime.ErrInvalidMediaParameter
	}
	return b.String(), nil
}

// partStructure builds the body structure for one MIME entity given its header
// and (transfer-encoded) body octets, recursing into multipart entities.
// defaultType applies when Content-Type is absent or malformed: text/plain
// (RFC 2045 §5.2), or message/rfc822 inside multipart/digest (RFC 2046 §5.1.5).
// depth is the entity's MIME nesting depth. An entity opaqueEntity reports,
// such as a multipart at maxMIMEDepth or a base64 message/rfc822, is described
// as an application/octet-stream leaf, as mimeNode.mediaType does for sections
// and SEARCH.
func partStructure(header textproto.MIMEHeader, body []byte, extension bool, defaultType string, depth int) string {
	if !knownTransferEncoding(header.Get("Content-Transfer-Encoding")) {
		return singlePartStructure("application", "octet-stream", nil, header, body, extension, depth)
	}
	mediatype, params, err := parseContentType(header.Get("Content-Type"))
	if !usableContentType(mediatype, params, err) {
		mediatype, params = defaultType, map[string]string{}
	}
	maintype, subtype := mediatype, ""
	if slash := strings.IndexByte(mediatype, '/'); slash >= 0 {
		maintype, subtype = mediatype[:slash], mediatype[slash+1:]
	}

	if opaqueEntity(mediatype, params, header.Get("Content-Transfer-Encoding"), depth) {
		return singlePartStructure("application", "octet-stream", nil, header, body, extension, depth)
	}
	if maintype == "multipart" && params["boundary"] != "" {
		return multipartStructure(subtype, params, header, body, extension, depth)
	}
	return singlePartStructure(maintype, subtype, params, header, body, extension, depth)
}

// multipartStructure builds the parenthesized structure for a multipart entity:
// the nested part structures concatenated with no separator, then the subtype,
// then (for BODYSTRUCTURE) the extension fields (RFC 3501 §7.4.2). header is the
// multipart's own header, the source of its body-ext-mpart disposition field.
func multipartStructure(subtype string, params map[string]string, header textproto.MIMEHeader, body []byte, extension bool, depth int) string {
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	childType := "text/plain"
	if strings.EqualFold(subtype, "digest") {
		childType = "message/rfc822"
	}
	var parts strings.Builder
	for {
		// BODY/BODYSTRUCTURE describe the transfer-encoded octets and lines
		// (RFC 3501 §7.4.2). NextPart transparently decodes quoted-printable
		// and removes its encoding header, corrupting all three metadata fields.
		p, err := mr.NextRawPart()
		if err != nil {
			break
		}
		pb, _ := io.ReadAll(p)
		parts.WriteString(partStructure(p.Header, pb, extension, childType, depth+1))
	}
	// A multipart with no decodable parts is still described as containing one
	// empty text/plain part, keeping the structure well-formed.
	if parts.Len() == 0 {
		parts.WriteString(singlePartStructure("text", "plain", nil, textproto.MIMEHeader{}, nil, extension, depth+1))
	}

	var b strings.Builder
	b.WriteByte('(')
	b.WriteString(parts.String())
	b.WriteByte(' ')
	b.WriteString(wire.QuoteUpper(subtype, "MIXED"))
	if extension {
		// body-ext-mpart: parameters, disposition, language, location. The
		// content-type parameters (including boundary) are reported; disposition is
		// read from the multipart's Content-Disposition when present; location is
		// taken from Content-Location; language from Content-Language.
		b.WriteByte(' ')
		b.WriteString(paramList(params))
		b.WriteByte(' ')
		b.WriteString(dispositionField(header))
		fmt.Fprintf(&b, " %s %s", languageField(header), wire.NilOrQuote(header.Get("Content-Location")))
	}
	b.WriteByte(')')
	return b.String()
}

// singlePartStructure builds the parenthesized structure for a non-multipart
// entity (RFC 3501 §7.4.2): type, subtype, parameters, id, description,
// encoding, size — plus a line count for text types, plus (for BODYSTRUCTURE)
// the single-part extension fields MD5/disposition/language/location.
func singlePartStructure(maintype, subtype string, params map[string]string, header textproto.MIMEHeader, body []byte, extension bool, depth int) string {
	if maintype == "" {
		maintype = "text"
	}
	if subtype == "" {
		subtype = "plain"
	}
	// A text part with no explicit charset defaults to US-ASCII (RFC 2045 §5.2).
	if strings.EqualFold(maintype, "text") {
		hasCharset := false
		for k := range params {
			if strings.EqualFold(k, "charset") {
				hasCharset = true
				break
			}
		}
		if !hasCharset {
			merged := map[string]string{"charset": "US-ASCII"}
			for k, v := range params {
				merged[k] = v
			}
			params = merged
		}
	}

	encoding := transferEncoding(header.Get("Content-Transfer-Encoding"))

	var b strings.Builder
	b.WriteByte('(')
	b.WriteString(wire.QuoteUpper(maintype, "TEXT"))
	b.WriteByte(' ')
	b.WriteString(wire.QuoteUpper(subtype, "PLAIN"))
	b.WriteByte(' ')
	b.WriteString(paramList(params))
	fmt.Fprintf(&b, " %s %s %s %d",
		wire.NilOrQuote(header.Get("Content-Id")),
		wire.NilOrQuote(header.Get("Content-Description")),
		wire.EncodeMailboxName(encoding),
		len(body))
	switch {
	case strings.EqualFold(maintype, "message") && strings.EqualFold(subtype, "rfc822"):
		// body-type-msg: after the basic fields, the envelope, body structure and
		// line count of the encapsulated message. The part body IS that message,
		// so it is parsed and described recursively (RFC 3501 §7.4.2).
		enc := string(body)
		fmt.Fprintf(&b, " %s %s %d",
			buildEncapsulatedEnvelope(enc),
			messageStructure(enc, extension, depth+1),
			countLines(body))
	case strings.EqualFold(maintype, "text"):
		// body-type-text adds a line count after the octet size.
		fmt.Fprintf(&b, " %d", countLines(body))
	}
	if extension {
		// body-ext-1part: MD5, disposition, language, location. Preserve the
		// supplied Content-MD5 and Content-Location; do not compute or verify a
		// digest, resolve a URI, or inherit fields from an enclosing entity.
		fmt.Fprintf(&b, " %s %s %s %s", wire.NilOrQuote(header.Get("Content-Md5")),
			dispositionField(header), languageField(header), wire.NilOrQuote(header.Get("Content-Location")))
	}
	b.WriteByte(')')
	return b.String()
}

// dispositionField renders a part's Content-Disposition as an IMAP body-fld-dsp:
// "(" disp-type SP body-fld-param ")" (RFC 3501 §7.4.2), e.g.
// ("ATTACHMENT" ("FILENAME" "doc.pdf")), with the disposition type and parameter
// names uppercased for consistency with the rest of the structure and parameter
// values preserved verbatim. Comments between tokens are ignored (RFC 2183 §2
// uses RFC 822 lexical rules). The disposition type is a single token, so a
// type/subtype pair is invalid. An absent or unparseable Content-Disposition
// yields NIL.
func dispositionField(header textproto.MIMEHeader) string {
	cd := header.Get("Content-Disposition")
	if cd == "" {
		return "NIL"
	}
	uncommented, err := uncommentMIME(cd)
	if err != nil {
		return "NIL"
	}
	disptype, params, err := mime.ParseMediaType(uncommented)
	if err != nil || disptype == "" || strings.Contains(disptype, "/") {
		return "NIL"
	}
	return "(" + wire.EncodeMailboxName(strings.ToUpper(disptype)) + " " + paramList(params) + ")"
}

// languageField renders a part's Content-Language as an IMAP body-fld-lang:
// nstring / "(" string *(SP string) ")" (RFC 3501 §7.4.2). The header is a
// comma-separated Language-List with optional CFWS (RFC 3282 §2): comments are
// dropped, each tag is trimmed and kept as supplied, and empty members are
// skipped. No tags is NIL, one tag a string, several a parenthesized list.
// Tags are not validated against the language-tag grammar.
func languageField(header textproto.MIMEHeader) string {
	var tags []string
	for _, v := range header.Values("Content-Language") {
		for _, tag := range strings.Split(stripComments(v), ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				tags = append(tags, wire.EncodeMailboxName(tag))
			}
		}
	}
	switch len(tags) {
	case 0:
		return "NIL"
	case 1:
		return tags[0]
	}
	return "(" + strings.Join(tags, " ") + ")"
}

// stripComments removes RFC 5322 §3.2.2 comments — parenthesized, nestable,
// with backslash quoted-pairs — from a structured header value. An unclosed
// comment runs to the end of the value.
func stripComments(s string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case depth > 0 && c == '\\':
			i++
		case c == '(':
			depth++
		case depth > 0 && c == ')':
			depth--
		case depth == 0:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// paramList renders a MIME parameter map as an IMAP parenthesized attribute/value
// list, e.g. ("CHARSET" "US-ASCII"), with attribute names uppercased and keys in
// sorted order for a deterministic result. An empty map yields NIL. Parameter
// names and values are strings, not nstrings: an empty value is "", never NIL.
// Reuse the astring encoder so decoded RFC 2231 values containing CR/LF or
// 8-bit octets use counted literals, including in disposition parameters.
func paramList(params map[string]string) string {
	if len(params) == 0 {
		return "NIL"
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('(')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(wire.EncodeMailboxName(strings.ToUpper(k)))
		b.WriteByte(' ')
		b.WriteString(wire.EncodeMailboxName(params[k]))
	}
	b.WriteByte(')')
	return b.String()
}

// countLines returns the number of text lines in body — the count RFC 3501's
// body-type-text requires. It counts line terminators, adding one for a final
// unterminated line.
func countLines(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	n := bytes.Count(body, []byte("\n"))
	if body[len(body)-1] != '\n' {
		n++
	}
	return n
}
