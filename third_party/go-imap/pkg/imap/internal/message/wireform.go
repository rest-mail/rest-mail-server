package message

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
//   - A multipart's boundary and delimiter lines stay in step (see wireBoundary).
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
	if !has8bit(node.header) && !needsWireForm(node.body) {
		return node.header + node.body
	}
	mt, params := node.mediaType()
	multipart := strings.HasPrefix(mt, "multipart/") && params["boundary"] != ""
	if multipart && (needsWireForm(node.body) || has8bit(params["boundary"])) {
		return wireMultipart(node, mt, params["boundary"], sessionUTF8)
	}
	header := wireHeader(node.header, sessionUTF8)
	switch {
	case !needsWireForm(node.body):
		return header + node.body
	case mt == "message/rfc822":
		return header + wireEntity(messageNode(node.body, node.depth+1), sessionUTF8)
	}
	return wireLeaf(node, header, mt, params, sessionUTF8)
}

// wireMultipart converts a multipart body part by part. A preamble or epilogue that
// holds 8-bit octets, which MIME readers ignore, has them escaped so a BODY[TEXT] of
// the multipart is 7-bit too. Delimiter lines stay what the boundary says, see
// wireBoundary, and the header is converted last, since that depends on the rest.
func wireMultipart(node mimeNode, mt, boundary string, sessionUTF8 bool) string {
	def := "text/plain"
	if mt == "multipart/digest" {
		def = "message/rfc822"
	}
	delimiters := multipartDelimiters(node.body, boundary)
	spans := partSpans(node.body, delimiters)
	// texts[i] is what stands before delimiter i, and the last is what follows them all.
	texts := make([]string, len(delimiters)+1)
	at := 0
	for i := range texts {
		end := len(node.body)
		if i < len(delimiters) {
			end = delimiters[i].at
		}
		if i == 0 || i > len(spans) {
			texts[i] = escape8bit(node.body[at:end])
		} else {
			span := spans[i-1]
			h, body := splitEntity(node.body[span[0]:span[1]])
			texts[i] = escape8bit(node.body[at:span[0]]) +
				wireEntity(mimeNode{header: h, body: body, defaultType: def, depth: node.depth + 1}, sessionUTF8) +
				escape8bit(node.body[span[1]:end])
		}
		at = end + len("--"+boundary)
	}

	spelled := wireBoundary(node.header, boundary, texts, sessionUTF8)
	header := node.header
	if spelled != boundary {
		header = headerWithBoundary(header, spelled)
	}
	var b strings.Builder
	b.WriteString(wireHeader(header, sessionUTF8))
	for i, text := range texts {
		if i > 0 {
			b.WriteString("--" + spelled)
		}
		b.WriteString(text)
	}
	return b.String()
}

// wireBoundary is the boundary a multipart goes out with. An 8-bit one has no 7-bit
// form a client could match against the stored lines, so the header and every delimiter
// line take the same new spelling, in the octets sent, which also spares a client
// reading RFC 2231 or a charset to find them. A session that enabled UTF8=ACCEPT reads
// valid UTF-8 as stored, in the field and on the lines alike.
//
// Either spelling must be nothing else in the body, or a client would split where the
// stored message doesn't: a converted boundary may equal text already there, and
// conversion may turn text into a stored boundary's delimiter. The texts already hold
// the delimiters of every nested multipart, so an outer boundary is what moves.
func wireBoundary(header, boundary string, texts []string, sessionUTF8 bool) string {
	field, _ := contentTypeField(header)
	keep := !has8bit(boundary) || strings.IndexByte(boundary, 0) >= 0 ||
		(sessionUTF8 && utf8.ValidString(boundary) && utf8.ValidString(field))
	if keep {
		for _, text := range texts {
			if len(multipartDelimiters(text, boundary)) > 0 {
				return freeBoundary(sevenBitBoundary(boundary), texts)
			}
		}
		return boundary
	}
	spelled := sevenBitBoundary(boundary)
	if startsLine(texts, spelled) {
		return freeBoundary(spelled, texts)
	}
	return spelled
}

// bareLineFeeds spells each bare LF as CRLF and leaves a bare CR alone, as a POP3 server
// does to the lines it sends (RFC 1939 §3).
var bareLineFeeds = strings.NewReplacer("\r\n", "\r\n", "\n", "\r\n")

// SevenBitBoundary is raw for a host that sends the body as stored, as POP3 does, with
// only the header downgraded. It has no 7-bit form of an 8-bit boundary a client could
// match against the stored lines, so a top-level multipart's Content-Type and delimiter
// lines take the spelling WireForm gives them, and nothing else changes. A part's header
// is sent as stored, so a nested boundary and its lines still match, and stay. Any other
// message comes back as it is. When the boundary moves, every bare LF of raw becomes
// CRLF, which makes its delimiters readable here and is what such a host then sends.
func SevenBitBoundary(raw string) string {
	canonical := bareLineFeeds.Replace(raw)
	node := messageNode(canonical, 0)
	mt, params := node.mediaType()
	boundary := params["boundary"]
	if !strings.HasPrefix(mt, "multipart/") || !has8bit(boundary) {
		return raw
	}
	delimiters := multipartDelimiters(node.body, boundary)
	// texts[i] is what stands before delimiter i, and the last is what follows them all.
	texts := make([]string, len(delimiters)+1)
	at := 0
	for i, d := range delimiters {
		texts[i] = node.body[at:d.at]
		at = d.at + len("--"+boundary)
	}
	texts[len(delimiters)] = node.body[at:]

	spelled := sevenBitBoundary(boundary)
	if startsLine(texts, spelled) {
		spelled = freeBoundary(spelled, texts)
	}
	header := headerWithBoundary(node.header, spelled)
	if header == node.header {
		return raw
	}
	var b strings.Builder
	b.Grow(len(canonical) + len(texts)*len(spelled))
	b.WriteString(header)
	for i, text := range texts {
		if i > 0 {
			b.WriteString("--" + spelled)
		}
		b.WriteString(text)
	}
	return b.String()
}

// sevenBitBoundary spells boundary in what RFC 2046 §5.1.1 allows one: every other
// octet is =XX, which leaves no 8-bit octet, no white space to lose at the end of a line
// and no quoting to get wrong, in at most 70 characters.
func sevenBitBoundary(boundary string) string {
	const digits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(boundary); i++ {
		if c := boundary[i]; isBoundaryChar(c) {
			b.WriteByte(c)
		} else {
			b.Write([]byte{'=', digits[c>>4], digits[c&0x0f]})
		}
	}
	if b.Len() > maxBoundary {
		return shortBoundary(b.String(), sha256.Sum256([]byte(boundary)))
	}
	return b.String()
}

// maxBoundary is the longest boundary RFC 2046 §5.1.1 allows.
const maxBoundary = 70

func isBoundaryChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		strings.IndexByte("'()+_,-./:=?", c) >= 0
}

// shortBoundary is spelled, cut short enough to hold a hash, with the hash after it.
func shortBoundary(spelled string, hash [sha256.Size]byte) string {
	const suffix = 13 // '_' and twelve hex digits
	if len(spelled) > maxBoundary-suffix {
		spelled = spelled[:maxBoundary-suffix]
	}
	return spelled + "_" + hex.EncodeToString(hash[:6])
}

// freeBoundary is spelled with a suffix that no line in texts starts with. The suffix
// hashes the texts, so a message can't hold the line it would need ahead of time, and
// it is the same each time, so sizes and offsets stay the same between FETCHes.
func freeBoundary(spelled string, texts []string) string {
	h := sha256.New()
	for _, text := range texts {
		h.Write([]byte(text))
	}
	hash := [sha256.Size]byte(h.Sum(nil))
	for {
		free := shortBoundary(spelled, hash)
		if !startsLine(texts, free) {
			return free
		}
		hash = sha256.Sum256(hash[:])
	}
}

// startsLine reports whether any line in texts starts with the delimiter of boundary,
// whatever follows it, which is what some readers take for one.
func startsLine(texts []string, boundary string) bool {
	delimiter := "--" + boundary
	for _, text := range texts {
		if strings.HasPrefix(text, delimiter) || strings.Contains(text, "\n"+delimiter) {
			return true
		}
	}
	return false
}

// contentTypeField is the first Content-Type of a header block, the one a reader
// takes, and its index among the block's fields.
func contentTypeField(header string) (string, int) {
	for i, field := range headerBlockFields(header) {
		if name, _, ok := strings.Cut(field, ":"); ok && strings.EqualFold(name, "Content-Type") {
			return field, i
		}
	}
	return "", -1
}

// headerWithBoundary rewrites the Content-Type of a header block with a new boundary.
// The result is 7-bit, because something that has to change its boundary has to change
// it wherever it is read, so wireHeader leaves it alone.
func headerWithBoundary(header, boundary string) string {
	field, at := contentTypeField(header)
	if at < 0 {
		return header
	}
	text, eol := strings.CutSuffix(field, "\r\n")
	name, value, _ := strings.Cut(text, ":")
	rewritten, ok := parameterField(name, unfoldHeader(value), boundary)
	if !ok {
		return header
	}
	if eol {
		rewritten += "\r\n"
	}
	fields := headerBlockFields(header)
	fields[at] = rewritten
	return strings.Join(fields, "")
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
