package message

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/quotedprintable"
	"strings"
	"unicode/utf8"
)

// WireForm is raw as FETCH sends it (RFC 3501 §7.4.2). The stored octets never
// change: this runs on the way out, and only what breaks the rule is rewritten,
// so a message or entity already 7-bit goes out byte for byte.
//
//   - Every header block, including a part's MIME header and an encapsulated
//     message's, is 7-bit (see wireHeader).
//   - A textual leaf that names a charset keeps its 8-bit data.
//   - Other 8-bit leaf data is transfer-encoded, BASE64 for non-textual data and
//     QUOTED-PRINTABLE for text with no charset, so no charset is guessed.
//   - A NUL in a header or a textual leaf stays, and FETCH refuses the message
//     (IMAP-ENV-12). In non-textual data it is encoded like any other octet.
//
// sessionUTF8 is whether the session enabled UTF8=ACCEPT (RFC 6855): valid UTF-8
// then stays in headers, and in a textual leaf with no charset, whose default
// there is UTF-8 (RFC 6532 §3).
func WireForm(raw string, sessionUTF8 bool) string {
	if !needsWireForm(raw) {
		return raw
	}
	return wireEntity(messageNode(raw, 0), sessionUTF8)
}

// needsWireForm reports whether s holds an octet WireForm might rewrite.
func needsWireForm(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 || s[i] == 0 {
			return true
		}
	}
	return false
}

func has8bit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

// wireEntity converts one entity. Types come from the stored header, which is
// what the sections and BODYSTRUCTURE of the same message describe.
func wireEntity(node mimeNode, sessionUTF8 bool) string {
	header := wireHeader(node.header, sessionUTF8)
	if !needsWireForm(node.body) {
		return header + node.body
	}
	mt, params := node.mediaType()
	switch {
	case mt == "message/rfc822":
		return header + wireEntity(messageNode(node.body, node.depth+1), sessionUTF8)
	case strings.HasPrefix(mt, "multipart/") && params["boundary"] != "":
		return header + wireMultipart(node, mt, params["boundary"], sessionUTF8)
	}
	return wireLeaf(node, header, mt, params, sessionUTF8)
}

// wireMultipart converts a multipart body part by part. Delimiter lines stay; a
// preamble or epilogue that holds 8-bit octets, which MIME readers ignore, has
// them escaped so a BODY[TEXT] of the multipart is 7-bit too.
func wireMultipart(node mimeNode, mt, boundary string, sessionUTF8 bool) string {
	def := "text/plain"
	if mt == "multipart/digest" {
		def = "message/rfc822"
	}
	var b strings.Builder
	at := 0
	for _, span := range multipartSpans(node.body, boundary) {
		b.WriteString(escape8bit(node.body[at:span[0]]))
		h, body := splitEntity(node.body[span[0]:span[1]])
		b.WriteString(wireEntity(mimeNode{header: h, body: body, defaultType: def, depth: node.depth + 1}, sessionUTF8))
		at = span[1]
	}
	b.WriteString(escape8bit(node.body[at:]))
	return b.String()
}

// wireLeaf converts a leaf whose body holds an 8-bit octet or a NUL. A body
// already declared base64 or quoted-printable keeps its encoding, so the client
// decodes the same data: base64 readers ignore octets outside the alphabet (RFC
// 2045 §6.8), so they go, and quoted-printable can spell them.
func wireLeaf(node mimeNode, header, mt string, params map[string]string, sessionUTF8 bool) string {
	textual := strings.HasPrefix(mt, "text/")
	if textual {
		switch {
		case strings.IndexByte(node.body, 0) >= 0:
			return header + node.body
		case params["charset"] != "":
			return header + node.body
		case sessionUTF8 && utf8.ValidString(node.body):
			return header + node.body
		}
	}
	cte := ""
	if v := HeaderFieldValues(node.header, "Content-Transfer-Encoding"); len(v) > 0 {
		cte = strings.TrimSpace(v[0])
	}
	switch transferEncoding(cte) {
	case "BASE64":
		return header + strings.Map(func(r rune) rune {
			if r >= 0x80 || r == 0 {
				return -1
			}
			return r
		}, node.body)
	case "QUOTED-PRINTABLE":
		return header + escape8bit(strings.ReplaceAll(node.body, "\x00", "=00"))
	case "7BIT", "8BIT", "BINARY":
		if textual {
			return setHeaderField(header, "Content-Transfer-Encoding", "quoted-printable") + quotedPrintable(node.body)
		}
	default:
		// An encoding nobody can remove makes the entity opaque data (RFC 2045
		// §6.4), which is what the header has to say once it is base64 instead.
		header = setHeaderField(header, "Content-Type", "application/octet-stream")
	}
	return setHeaderField(header, "Content-Transfer-Encoding", "base64") + base64Lines(node.body)
}

// metadataText is an ENVELOPE or BODYSTRUCTURE string a decoder produced: a name
// from an encoded word, or a parameter from RFC 2231. Those strings have nowhere
// to name a charset (RFC 3501 §4.3.1), so 8-bit text goes out as encoded words,
// separated by a space so the string needs no literal. After UTF8=ACCEPT (RFC 6855)
// valid UTF-8 is the agreed charset and stays. A NUL stays too, so FETCH refuses
// the message rather than hiding it.
func metadataText(text string, sessionUTF8 bool) string {
	if !has8bit(text) || strings.IndexByte(text, 0) >= 0 || (sessionUTF8 && utf8.ValidString(text)) {
		return text
	}
	return strings.Join(encodedWords(text), " ")
}

// escape8bit spells each 8-bit octet =XX and leaves every other octet alone.
func escape8bit(s string) string {
	if !has8bit(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			fmt.Fprintf(&b, "=%02X", s[i])
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func quotedPrintable(body string) string {
	var b bytes.Buffer
	w := quotedprintable.NewWriter(&b)
	// A bytes.Buffer cannot fail.
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	return b.String()
}

// base64Lines encodes body in lines of 76 characters (RFC 2045 §6.8). A body
// that ended in a line break still does, so the message keeps its shape.
func base64Lines(body string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	var b strings.Builder
	b.Grow(len(enc) + len(enc)/38 + 2)
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	if strings.HasSuffix(body, "\r\n") {
		b.WriteString("\r\n")
	}
	return b.String()
}

// setHeaderField replaces the named field of a header block, or adds it before
// the blank line that ends the block.
func setHeaderField(header, name, value string) string {
	field := name + ": " + value + "\r\n"
	var b strings.Builder
	placed := false
	for _, f := range headerBlockFields(header) {
		if fieldName, _, ok := strings.Cut(f, ":"); ok && strings.EqualFold(strings.TrimSpace(fieldName), name) {
			if !placed {
				b.WriteString(field)
				placed = true
			}
			continue
		}
		if !placed && f == "\r\n" {
			b.WriteString(field)
			placed = true
		}
		b.WriteString(f)
	}
	if !placed {
		b.WriteString(field)
	}
	return b.String()
}

// headerBlockFields splits a header block into its fields, each with its
// continuation lines and final CRLF. The blank line that ends the block is an
// element of its own, so joining the result gives the block back.
func headerBlockFields(header string) []string {
	var fields []string
	start := 0
	for at := 0; at < len(header); {
		if at > start && header[at] != ' ' && header[at] != '\t' {
			fields = append(fields, header[start:at])
			start = at
		}
		i := strings.Index(header[at:], "\r\n")
		if i < 0 {
			break
		}
		at += i + 2
	}
	if start < len(header) {
		fields = append(fields, header[start:])
	}
	return fields
}

// wireHeader makes a header block 7-bit, field by field; a field already 7-bit
// is not touched. In a UTF8=ACCEPT session, a field of valid UTF-8 stays. A field
// holding a NUL stays too, so FETCH refuses the message rather than hiding it.
func wireHeader(header string, sessionUTF8 bool) string {
	if !has8bit(header) {
		return header
	}
	var b strings.Builder
	b.Grow(len(header) + len(header)/4)
	for _, field := range headerBlockFields(header) {
		if !has8bit(field) || strings.IndexByte(field, 0) >= 0 || (sessionUTF8 && utf8.ValidString(field)) {
			b.WriteString(field)
			continue
		}
		b.WriteString(sevenBitField(field))
	}
	return b.String()
}

// sevenBitField rewrites one field holding an 8-bit octet. Content-Type and
// Content-Disposition keep their parameters, see ParameterField, because an
// encoded word may not appear in a parameter (RFC 2047 §5); anything else has its
// 8-bit text turned into encoded words. A line that is not a field at all is
// encoded whole, so its octets survive and a reader still finds no field in it.
func sevenBitField(field string) string {
	text, eol := strings.CutSuffix(field, "\r\n")
	name, value, ok := strings.Cut(text, ":")
	if !ok || has8bit(name) {
		out := EncodeWords(unfoldHeader(text))
		if eol {
			out += "\r\n"
		}
		return out
	}
	var out string
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "content-type", "content-disposition":
		if rewritten, ok := ParameterField(name, unfoldHeader(value)); ok {
			out = rewritten
			break
		}
		fallthrough
	default:
		out = name + ":" + encodeHeaderValue(len(name)+1, value)
	}
	if eol {
		out += "\r\n"
	}
	return out
}

func unfoldHeader(s string) string {
	return strings.ReplaceAll(s, "\r\n", "")
}

// isAttributeChar reports whether c may stand unescaped in an RFC 2231 extended
// value: the MIME token characters less '*', "'" and '%'.
func isAttributeChar(c byte) bool {
	return c > 0x20 && c < 0x7f && !strings.ContainsRune(`()<>@,;:\"/[]?=*'%`, rune(c))
}

// encodeHeaderValue turns the 8-bit runs of a field value into encoded words and
// leaves everything else, folding included, as it was. Tokens are split on white
// space, a quoted-string counting as one token. Adjacent 8-bit tokens make one
// run, since white space between encoded words is not displayed (RFC 2047 §6.2).
// Punctuation around a run stays outside it, so a comment or angle address keeps
// its delimiters, and quotes around one go, because an encoded word may not stand
// in a quoted-string (RFC 2047 §5).
func encodeHeaderValue(column int, value string) string {
	var b strings.Builder
	tokens := headerTokens{value: value, unclosed: len(value)}
	line := column
	write := func(s string) {
		b.WriteString(s)
		if i := strings.LastIndex(s, "\r\n"); i >= 0 {
			line = len(s) - i - 2
		} else {
			line += len(s)
		}
	}
	for i := 0; i < len(value); {
		if !isHeaderSpace(value[i]) && !has8bit(tokens.at(i)) {
			token := tokens.at(i)
			write(token)
			i += len(token)
			continue
		}
		if isHeaderSpace(value[i]) {
			j := i
			for j < len(value) && isHeaderSpace(value[j]) {
				j++
			}
			write(value[i:j])
			i = j
			continue
		}
		end := i + len(tokens.at(i))
		for {
			j := end
			for j < len(value) && isHeaderSpace(value[j]) {
				j++
			}
			if j == end || j == len(value) || !has8bit(tokens.at(j)) {
				break
			}
			end = j + len(tokens.at(j))
		}
		lead, run, trail := peelPunctuation(unfoldHeader(value[i:end]))
		write(lead)
		words := encodedWords(run)
		for k, word := range words {
			if k > 0 {
				write("\r\n ")
			} else if line+len(word)+len(trail) > 76 && line > 1 {
				write("\r\n ")
			}
			write(word)
		}
		write(trail)
		i = end
	}
	return b.String()
}

func isHeaderSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// headerTokens reads the tokens of one field value. unclosed is the offset of the
// first '"' found to have no closing quote, so that scan runs only once: every later
// '"' in the value has none either. A scan that reached a later '"' would have closed
// on it, so it must have skipped it as a quoted-pair, and a scan from just after that
// '"' follows the same octets to the end of the value.
type headerTokens struct {
	value    string
	unclosed int
}

// at is the token starting at offset start: up to white space, a quoted-string that
// closes counting as part of it, spaces and all.
func (t *headerTokens) at(start int) string {
	i := start
	for i < len(t.value) && !isHeaderSpace(t.value[i]) {
		if t.value[i] == '"' && i < t.unclosed {
			closes := t.closingQuote(i)
			if closes < 0 {
				t.unclosed = i
			} else {
				i = closes
			}
		}
		i++
	}
	return t.value[start:i]
}

// closingQuote is the offset of the '"' that closes the one at open, or -1.
func (t *headerTokens) closingQuote(open int) int {
	for j := open + 1; j < len(t.value); j++ {
		if t.value[j] == '\\' {
			j++
		} else if t.value[j] == '"' {
			return j
		}
	}
	return -1
}

// unescapeQuotedPairs reads the quoted-pairs of a quoted-string.
var unescapeQuotedPairs = strings.NewReplacer(`\\`, `\`, `\"`, `"`)

// peelPunctuation splits the structure around a run from its text. Quotes are
// dropped with the quoted-pairs they allowed.
func peelPunctuation(run string) (lead, text, trail string) {
	start, end := 0, len(run)
	for start < end && strings.IndexByte(`(<"`, run[start]) >= 0 {
		start++
	}
	for end > start && strings.IndexByte(`)>,;:"`, run[end-1]) >= 0 {
		end--
	}
	lead, text, trail = run[:start], run[start:end], run[end:]
	if strings.Contains(lead, `"`) || strings.Contains(trail, `"`) {
		lead = strings.ReplaceAll(lead, `"`, "")
		trail = strings.ReplaceAll(trail, `"`, "")
		text = unescapeQuotedPairs.Replace(text)
	}
	return lead, text, trail
}

// EncodeWords renders text as folded RFC 2047 encoded words.
func EncodeWords(text string) string {
	return strings.Join(encodedWords(text), "\r\n ")
}

// wordCharset names what text is: UTF-8 when it is valid UTF-8. Anything else is
// UNKNOWN-8BIT (RFC 1428), so the octets survive and no charset is guessed.
func wordCharset(text string) string {
	if utf8.ValidString(text) {
		return "UTF-8"
	}
	return "UNKNOWN-8BIT"
}

// encodedWords spells text as encoded words of at most 75 characters (RFC 2047
// §2), never splitting a UTF-8 character across two (§5).
func encodedWords(text string) []string {
	charset := wordCharset(text)
	open := "=?" + charset + "?Q?"
	const closing = "?="
	var words []string
	var cur, enc []byte
	for i := 0; i < len(text); {
		size := 1
		if charset == "UTF-8" {
			_, size = utf8.DecodeRuneInString(text[i:])
		}
		enc = appendQEncoded(enc[:0], text[i:i+size])
		if len(cur) > 0 && len(open)+len(cur)+len(enc)+len(closing) > 75 {
			words = append(words, open+string(cur)+closing)
			cur = cur[:0]
		}
		cur = append(cur, enc...)
		i += size
	}
	return append(words, open+string(cur)+closing)
}

// qEncode is the Q encoding restricted to what RFC 2047 §5(3) allows wherever an
// encoded word may stand.
func qEncode(s string) string {
	return string(appendQEncoded(nil, s))
}

func appendQEncoded(b []byte, s string) []byte {
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			b = append(b, '_')
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', strings.IndexByte("!*+-/", c) >= 0:
			b = append(b, c)
		default:
			b = append(b, '=', hex[c>>4], hex[c&0x0f])
		}
	}
	return b
}
