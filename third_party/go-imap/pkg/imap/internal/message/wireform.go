package message

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"mime/quotedprintable"
	"strings"
	"sync"
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
//   - A NUL in a header or a textual leaf goes out as a space, one octet for one
//     (IMAP-ENV-12). Where a NUL is data, see nulIsData, it is encoded like any
//     other octet instead, so the client decodes the exact octets.
//   - A multipart's boundary and delimiter lines stay in step (see wireBoundary).
//
// sessionUTF8 is whether the session enabled UTF8=ACCEPT (RFC 6855): valid UTF-8
// then stays in headers, and in a textual leaf with no charset, whose default
// there is UTF-8 (RFC 6532 §3).
func WireForm(raw string, sessionUTF8 bool) string {
	if !needsWireForm(raw) {
		return raw
	}
	var b strings.Builder
	b.Grow(len(raw) + len(raw)/4)
	wireEntity(&b, messageNode(raw, 0), sessionUTF8)
	return b.String()
}

// WireFormSize is len(WireForm(raw, sessionUTF8)), and holds nothing that grows with
// the data: the same conversion runs, into a counter instead of a buffer, so the two
// can't disagree. A store measures every message this way, see package wireform. A
// message that may have to move a boundary is the exception, see wireCounter.
func WireFormSize(raw string, sessionUTF8 bool) int {
	if !needsWireForm(raw) {
		return len(raw)
	}
	var size wireCounter
	wireEntity(&size, messageNode(raw, 0), sessionUTF8)
	if size.collided {
		return len(WireForm(raw, sessionUTF8))
	}
	return size.n
}

// WireFormJoinedSize is WireFormSize(block+body, sessionUTF8) without joining them.
// It is for a host that rewrites a header block and keeps the stored body.
func WireFormJoinedSize(block, body string, sessionUTF8 bool) int {
	end, blank := headerEnd(block)
	if end != len(block) || !blank {
		// The block doesn't end where the header does, so the body isn't what follows it.
		return WireFormSize(block+body, sessionUTF8)
	}
	if !needsWireForm(block) && !needsWireForm(body) {
		return len(block) + len(body)
	}
	var size wireCounter
	wireEntity(&size, mimeNode{header: block, body: body, message: true, defaultType: "text/plain"}, sessionUTF8)
	if size.collided {
		return len(WireForm(block+body, sessionUTF8))
	}
	return size.n
}

// wireSink takes a conversion's output: a strings.Builder to send it, a wireCounter to
// measure it.
type wireSink interface {
	io.Writer
	io.StringWriter
}

// wireCounter is a wireSink that keeps only the count, and watches the lines going past
// for the delimiters of the multiparts it is inside. Where one turns up, the boundary has
// to move (see wireBoundary), to a spelling that hashes the converted text, so counting
// can't go on without that text: collided says the conversion has to run for real. The
// watch is wider than wireBoundary's test, which only costs that run.
type wireCounter struct {
	n        int
	watching []lineWatch
	collided bool
}

func (c *wireCounter) Write(p []byte) (int, error) {
	c.n += len(p)
	for i := range c.watching {
		for _, b := range p {
			c.collided = c.watching[i].step(b) || c.collided
		}
	}
	return len(p), nil
}

func (c *wireCounter) WriteString(s string) (int, error) {
	c.n += len(s)
	for i := range c.watching {
		c.collided = c.watching[i].scan(s) || c.collided
	}
	return len(s), nil
}

// lineWatch looks for a line that starts with delimiter. matched is how much of it the
// line has matched so far, or -1 once the line has failed.
type lineWatch struct {
	delimiter string
	matched   int
}

func (l *lineWatch) step(b byte) bool {
	if l.matched >= 0 {
		if b == l.delimiter[l.matched] {
			l.matched++
			if l.matched == len(l.delimiter) {
				l.matched = -1
				return true
			}
		} else {
			l.matched = -1
		}
	}
	if b == '\n' {
		l.matched = 0
	}
	return false
}

func (l *lineWatch) scan(s string) bool {
	found := false
	for i := 0; i < len(s); i++ {
		if l.matched < 0 {
			next := strings.IndexByte(s[i:], '\n')
			if next < 0 {
				return found
			}
			i += next
		}
		found = l.step(s[i]) || found
	}
	return found
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

// SpaceNUL is raw with no NUL in a header or in text, for a protocol with nothing like
// BINARY or a transfer-encoding conversion. Every octet stays where it was, so a size
// measured from raw stays true. Where a NUL is data, see nulIsData, the entity is left
// as it is. A message holding no NUL comes back byte for byte, and one that does is
// built once, in the one buffer returned.
func SpaceNUL(raw string) []byte {
	if strings.IndexByte(raw, 0) < 0 {
		return []byte(raw)
	}
	out := byteSink(make([]byte, 0, len(raw)))
	spaceNULEntity(&out, messageNode(raw, 0))
	return out
}

func spaceNULEntity(w wireSink, node mimeNode) {
	writeSpaced(w, node.header)
	if strings.IndexByte(node.body, 0) < 0 {
		_, _ = w.WriteString(node.body)
		return
	}
	mt, params := node.mediaType()
	switch {
	case mt == "message/rfc822":
		spaceNULEntity(w, messageNode(node.body, node.depth+1))
	case strings.HasPrefix(mt, "multipart/") && params["boundary"] != "":
		def := "text/plain"
		if mt == "multipart/digest" {
			def = "message/rfc822"
		}
		at := 0
		for _, span := range partSpans(node.body, multipartDelimiters(node.body, params["boundary"])) {
			writeSpaced(w, node.body[at:span[0]])
			h, body := splitEntity(node.body[span[0]:span[1]])
			spaceNULEntity(w, mimeNode{header: h, body: body, defaultType: def, depth: node.depth + 1})
			at = span[1]
		}
		writeSpaced(w, node.body[at:])
	case nulIsData(mt, params):
		_, _ = w.WriteString(node.body)
	default:
		writeSpaced(w, node.body)
	}
}

// byteSink is a wireSink that appends to a byte slice.
type byteSink []byte

func (b *byteSink) Write(p []byte) (int, error) {
	*b = append(*b, p...)
	return len(p), nil
}

func (b *byteSink) WriteString(s string) (int, error) {
	*b = append(*b, s...)
	return len(s), nil
}

// nulIsData reports whether a NUL in a leaf of this type means something, so a space
// would corrupt it: anything not text, which includes an encoding nobody can remove
// (see mediaType), and text in a charset whose characters hold NUL octets.
func nulIsData(mt string, params map[string]string) bool {
	if !strings.HasPrefix(mt, "text/") {
		return true
	}
	charset := strings.ToLower(params["charset"])
	for _, wide := range []string{"utf-16", "utf16", "utf-32", "utf32", "ucs-2", "ucs2", "ucs-4", "ucs4", "iso-10646-ucs-", "csucs4", "csunicode"} {
		if strings.HasPrefix(charset, wide) {
			return true
		}
	}
	return false
}

// writeSpaced writes s with a space for each NUL, and holds no copy of it.
func writeSpaced(w wireSink, s string) {
	for {
		i := strings.IndexByte(s, 0)
		if i < 0 {
			_, _ = w.WriteString(s)
			return
		}
		_, _ = w.WriteString(s[:i])
		_, _ = w.WriteString(" ")
		s = s[i+1:]
	}
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
func wireEntity(w wireSink, node mimeNode, sessionUTF8 bool) {
	if !has8bit(node.header) && !needsWireForm(node.body) {
		writeSpaced(w, node.header)
		_, _ = w.WriteString(node.body)
		return
	}
	mt, params := node.mediaType()
	multipart := strings.HasPrefix(mt, "multipart/") && params["boundary"] != ""
	switch {
	case multipart && (needsWireForm(node.body) || has8bit(params["boundary"])):
		wireMultipart(w, node, mt, params["boundary"], sessionUTF8)
	case !needsWireForm(node.body):
		wireHeader(w, node.header, sessionUTF8)
		_, _ = w.WriteString(node.body)
	case mt == "message/rfc822":
		wireHeader(w, node.header, sessionUTF8)
		wireEntity(w, messageNode(node.body, node.depth+1), sessionUTF8)
	default:
		wireLeaf(w, node, mt, params, sessionUTF8)
	}
}

// wireMultipart converts a multipart body part by part. A preamble or epilogue that
// holds 8-bit octets, which MIME readers ignore, has them escaped so a BODY[TEXT] of
// the multipart is 7-bit too. Delimiter lines stay what the boundary says, see
// wireBoundary, and the header is converted last, since that depends on the rest.
func wireMultipart(w wireSink, node mimeNode, mt, boundary string, sessionUTF8 bool) {
	parts := multipartTexts{node: node, def: "text/plain", boundary: boundary, sessionUTF8: sessionUTF8}
	if mt == "multipart/digest" {
		parts.def = "message/rfc822"
	}
	parts.delimiters = multipartDelimiters(node.body, boundary)
	parts.spans = partSpans(node.body, parts.delimiters)
	if counter, ok := w.(*wireCounter); ok {
		parts.count(counter)
		return
	}
	// texts[i] is what stands before delimiter i, and the last is what follows them all.
	texts := make([]string, len(parts.delimiters)+1)
	for i := range texts {
		var text strings.Builder
		parts.write(&text, i)
		texts[i] = text.String()
	}

	spelled := wireBoundary(node.header, boundary, texts, sessionUTF8)
	header := node.header
	if spelled != boundary {
		header = headerWithBoundary(header, spelled)
	}
	wireHeader(w, header, sessionUTF8)
	for i, text := range texts {
		if i > 0 {
			_, _ = w.WriteString("--")
			_, _ = w.WriteString(spelled)
		}
		_, _ = w.WriteString(text)
	}
}

// multipartTexts is what a multipart's delimiter lines divide its body into.
type multipartTexts struct {
	node        mimeNode
	def         string
	boundary    string
	sessionUTF8 bool
	delimiters  []multipartDelimiter
	spans       [][2]int
}

// write converts text i: what stands before delimiter i, or for the last, what follows
// them all.
func (m *multipartTexts) write(w wireSink, i int) {
	body := m.node.body
	at := 0
	if i > 0 {
		at = m.delimiters[i-1].at + len("--"+m.boundary)
	}
	end := len(body)
	if i < len(m.delimiters) {
		end = m.delimiters[i].at
	}
	if i == 0 || i > len(m.spans) {
		writeEscaped8bit(w, body[at:end], false)
		return
	}
	span := m.spans[i-1]
	writeEscaped8bit(w, body[at:span[0]], false)
	h, part := splitEntity(body[span[0]:span[1]])
	wireEntity(w, mimeNode{header: h, body: part, defaultType: m.def, depth: m.node.depth + 1}, m.sessionUTF8)
	writeEscaped8bit(w, body[span[1]:end], false)
}

// count is the conversion without holding the texts. With no line in them that starts
// with the delimiter, wireBoundary keeps the spelling it starts from, so the header can
// be counted first, as it is sent.
func (m *multipartTexts) count(c *wireCounter) {
	spelled := m.boundary
	if !boundaryStays(m.node.header, m.boundary, m.sessionUTF8) {
		spelled = sevenBitBoundary(m.boundary)
	}
	header := m.node.header
	if spelled != m.boundary {
		header = headerWithBoundary(header, spelled)
	}
	wireHeader(c, header, m.sessionUTF8)
	delimiter := "--" + spelled
	if strings.IndexByte(delimiter, '\n') >= 0 {
		// A delimiter holding a line break can't be watched for line by line.
		c.collided = true
	}
	outer := len(c.watching)
	c.watching = append(c.watching, lineWatch{delimiter: delimiter})
	for i := range len(m.delimiters) + 1 {
		if i > 0 {
			watching := c.watching
			c.watching = watching[:outer]
			_, _ = c.WriteString(delimiter)
			c.watching = watching
		}
		// A text starts a line of its own, whatever stood before it.
		c.watching[outer].matched = 0
		m.write(c, i)
	}
	c.watching = c.watching[:outer]
}

// boundaryStays reports whether a multipart's boundary goes out as stored, unless that
// would collide with something in its body. A NUL can't go out, and a space for it could
// end the boundary or spell another line, so that one moves like an 8-bit one.
func boundaryStays(header, boundary string, sessionUTF8 bool) bool {
	if strings.IndexByte(boundary, 0) >= 0 {
		return false
	}
	if !has8bit(boundary) {
		return true
	}
	field, _ := contentTypeField(header)
	return sessionUTF8 && utf8.ValidString(boundary) && utf8.ValidString(field)
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
	var scan boundaryScan
	if boundaryStays(header, boundary, sessionUTF8) {
		for _, text := range texts {
			if len(scan.delimiters(text, boundary)) > 0 {
				return scan.freeBoundary(sevenBitBoundary(boundary), texts)
			}
		}
		return boundary
	}
	spelled := sevenBitBoundary(boundary)
	if scan.startsLine(texts, spelled) {
		return scan.freeBoundary(spelled, texts)
	}
	return spelled
}

// bareLineFeeds spells each bare LF as CRLF and leaves a bare CR alone, as a POP3 server
// does to the lines it sends (RFC 1939 §3).
var bareLineFeeds = strings.NewReplacer("\r\n", "\r\n", "\n", "\r\n")

// withoutBareLineFeeds applies bareLineFeeds, and copies nothing when there are none.
func withoutBareLineFeeds(s string) string {
	if strings.Count(s, "\n") == strings.Count(s, "\r\n") {
		return s
	}
	return bareLineFeeds.Replace(s)
}

// SevenBitBoundary is raw for a host that sends the body as stored, as POP3 does, with
// only the header downgraded. It has no 7-bit form of an 8-bit boundary a client could
// match against the stored lines, so a top-level multipart's Content-Type and delimiter
// lines take the spelling WireForm gives them, and nothing else changes. A part's header
// is sent as stored, so a nested boundary and its lines still match, and stay. Any other
// message comes back as it is. When the boundary moves, every bare LF of raw becomes
// CRLF, which makes its delimiters readable here and is what such a host then sends.
func SevenBitBoundary(raw string) string {
	var scan boundaryScan
	return scan.sevenBitBoundary(raw)
}

func (s *boundaryScan) sevenBitBoundary(raw string) string {
	// Only the header says whether the rest needs reading.
	mt, params := messageNode(withoutBareLineFeeds(raw[:headerBlockEnd(raw)]), 0).mediaType()
	boundary := params["boundary"]
	if !strings.HasPrefix(mt, "multipart/") || !has8bit(boundary) {
		return raw
	}
	canonical := withoutBareLineFeeds(raw)
	node := messageNode(canonical, 0)
	_, params = node.mediaType()
	boundary = params["boundary"]
	delimiters := s.delimiters(node.body, boundary)
	// texts[i] is what stands before delimiter i, and the last is what follows them all.
	texts := make([]string, len(delimiters)+1)
	at := 0
	for i, d := range delimiters {
		texts[i] = node.body[at:d.at]
		at = d.at + len("--"+boundary)
	}
	texts[len(delimiters)] = node.body[at:]

	spelled := sevenBitBoundary(boundary)
	if s.startsLine(texts, spelled) {
		spelled = s.freeBoundary(spelled, texts)
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

// headerBlockEnd is where the blank line that ends the header ends, whichever line
// breaks it uses, or the end of raw when there is none.
func headerBlockEnd(raw string) int {
	if strings.HasPrefix(raw, "\n") {
		return 1
	}
	if strings.HasPrefix(raw, "\r\n") {
		return 2
	}
	for at := 0; ; {
		i := strings.IndexByte(raw[at:], '\n')
		if i < 0 {
			return len(raw)
		}
		at += i + 1
		if strings.HasPrefix(raw[at:], "\n") {
			return at + 1
		}
		if strings.HasPrefix(raw[at:], "\r\n") {
			return at + 2
		}
	}
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
func (s *boundaryScan) freeBoundary(spelled string, texts []string) string {
	h := sha256.New()
	for _, text := range texts {
		s.steps += len(text)
		h.Write([]byte(text))
	}
	hash := [sha256.Size]byte(h.Sum(nil))
	for {
		free := shortBoundary(spelled, hash)
		if !s.startsLine(texts, free) {
			return free
		}
		hash = sha256.Sum256(hash[:])
	}
}

// startsLine reports whether any line in texts starts with the delimiter of boundary,
// whatever follows it, which is what some readers take for one.
func (s *boundaryScan) startsLine(texts []string, boundary string) bool {
	delimiter := "--" + boundary
	for _, text := range texts {
		s.steps += len(text)
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
// 2045 §6.8), so they go, and quoted-printable can spell them. A NUL in text is a
// space on any of these paths; text whose NUL is data is treated as data.
func wireLeaf(w wireSink, node mimeNode, mt string, params map[string]string, sessionUTF8 bool) {
	textual := strings.HasPrefix(mt, "text/") && (strings.IndexByte(node.body, 0) < 0 || !nulIsData(mt, params))
	if textual && (!has8bit(node.body) || params["charset"] != "" || (sessionUTF8 && utf8.ValidString(node.body))) {
		wireHeader(w, node.header, sessionUTF8)
		writeSpaced(w, node.body)
		return
	}
	cte := ""
	if namesContentField(node.header) {
		if v := HeaderFieldValues(node.header, "Content-Transfer-Encoding"); len(v) > 0 {
			cte = strings.TrimSpace(v[0])
		}
	}
	opaque := false
	switch transferEncoding(cte) {
	case "BASE64":
		wireHeader(w, node.header, sessionUTF8)
		writeWithout8bit(w, node.body)
		return
	case "QUOTED-PRINTABLE":
		wireHeader(w, node.header, sessionUTF8)
		writeEscaped8bit(w, node.body, !textual)
		return
	case "7BIT", "8BIT", "BINARY":
		if textual {
			wireHeader(w, node.header, sessionUTF8, fieldChange{"Content-Transfer-Encoding", "quoted-printable"})
			writeQuotedPrintable(w, node.body)
			return
		}
	default:
		// An encoding nobody can remove makes the entity opaque data (RFC 2045
		// §6.4), which is what the header has to say once it is base64 instead.
		opaque = true
	}
	if opaque {
		wireHeader(w, node.header, sessionUTF8, fieldChange{"Content-Type", "application/octet-stream"}, fieldChange{"Content-Transfer-Encoding", "base64"})
	} else {
		wireHeader(w, node.header, sessionUTF8, fieldChange{"Content-Transfer-Encoding", "base64"})
	}
	writeBase64Lines(w, node.body)
}

// metadataText is an ENVELOPE or BODYSTRUCTURE string a decoder produced: a name
// from an encoded word, or a parameter from RFC 2231. Those strings have nowhere
// to name a charset (RFC 3501 §4.3.1), so 8-bit text goes out as encoded words,
// separated by a space so the string needs no literal. After UTF8=ACCEPT (RFC 6855)
// valid UTF-8 is the agreed charset and stays. A decoded NUL is a space, as it is in
// a header, so it can't reach the client through an encoded word either.
func metadataText(text string, sessionUTF8 bool) string {
	text = strings.ReplaceAll(text, "\x00", " ")
	if !has8bit(text) || (sessionUTF8 && utf8.ValidString(text)) {
		return text
	}
	return strings.Join(encodedWords(text), " ")
}

// writeEscaped8bit spells each 8-bit octet =XX, and a NUL =00 when nul is set or a
// space when it isn't, and leaves every other octet alone.
func writeEscaped8bit(w wireSink, s string, nul bool) {
	const digits = "0123456789ABCDEF"
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0 && !nul {
			_, _ = w.WriteString(s[start:i])
			_, _ = w.WriteString(" ")
			start = i + 1
			continue
		}
		if c >= 0x80 || c == 0 {
			_, _ = w.WriteString(s[start:i])
			_, _ = w.WriteString("=")
			_, _ = w.WriteString(digits[c>>4 : c>>4+1])
			_, _ = w.WriteString(digits[c&0x0f : c&0x0f+1])
			start = i + 1
		}
	}
	_, _ = w.WriteString(s[start:])
}

// writeWithout8bit leaves out each 8-bit octet and each NUL.
func writeWithout8bit(w wireSink, s string) {
	start := 0
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 0x80 || c == 0 {
			_, _ = w.WriteString(s[start:i])
			start = i + 1
		}
	}
	_, _ = w.WriteString(s[start:])
}

// wireChunk is how much of a body a transfer encoding holds at once.
const wireChunk = 1 << 12

// writeQuotedPrintable encodes text, so a NUL goes in as a space rather than as =00.
func writeQuotedPrintable(w wireSink, body string) {
	q := quotedprintable.NewWriter(w)
	chunk := make([]byte, min(len(body), wireChunk))
	for len(body) > 0 {
		n := copy(chunk, body)
		for i, c := range chunk[:n] {
			if c == 0 {
				chunk[i] = ' '
			}
		}
		// The sink cannot fail.
		_, _ = q.Write(chunk[:n])
		body = body[n:]
	}
	_ = q.Close()
}

// writeBase64Lines encodes body in lines of 76 characters (RFC 2045 §6.8). A body
// that ended in a line break still does, so the message keeps its shape.
func writeBase64Lines(w wireSink, body string) {
	const lineData, lineLen = 57, 76
	crlf := strings.HasSuffix(body, "\r\n")
	lines := min(len(body)/lineData+1, wireChunk/lineLen)
	chunk := make([]byte, lines*(lineLen+2))
	var data [lineData]byte
	used := 0
	for len(body) > 0 {
		n := copy(data[:], body)
		body = body[n:]
		size := base64.StdEncoding.EncodedLen(n)
		base64.StdEncoding.Encode(chunk[used:used+size], data[:n])
		used += size
		if len(body) > 0 {
			used += copy(chunk[used:], "\r\n")
		}
		if used+lineLen+2 > len(chunk) {
			_, _ = w.Write(chunk[:used])
			used = 0
		}
	}
	_, _ = w.Write(chunk[:used])
	if crlf {
		_, _ = w.WriteString("\r\n")
	}
}

// fieldChange replaces a named field of a header block, or adds it before the blank
// line that ends the block.
type fieldChange struct{ name, value string }

func (c fieldChange) write(w wireSink) {
	_, _ = w.WriteString(c.name)
	_, _ = w.WriteString(": ")
	_, _ = w.WriteString(c.value)
	_, _ = w.WriteString("\r\n")
}

// nextHeaderField is where the field at start ends: its continuation lines and final
// CRLF belong to it. The blank line that ends a block is a field of its own.
func nextHeaderField(header string, start int) int {
	for at := start; at < len(header); {
		if at > start && header[at] != ' ' && header[at] != '\t' {
			return at
		}
		i := strings.Index(header[at:], "\r\n")
		if i < 0 {
			break
		}
		at += i + 2
	}
	return len(header)
}

// headerBlockFields splits a header block into its fields, each with its
// continuation lines and final CRLF, so joining the result gives the block back.
func headerBlockFields(header string) []string {
	var fields []string
	for start := 0; start < len(header); {
		end := nextHeaderField(header, start)
		fields = append(fields, header[start:end])
		start = end
	}
	return fields
}

// wireHeader writes a header block 7-bit, field by field; a field already 7-bit
// is not touched. In a UTF8=ACCEPT session, a field of valid UTF-8 stays. A NUL is a
// space before any of that, so an encoded word never spells one. changes then apply
// to what that leaves.
func wireHeader(w wireSink, header string, sessionUTF8 bool, changes ...fieldChange) {
	if len(changes) == 0 && !has8bit(header) {
		writeSpaced(w, header)
		return
	}
	var placed [2]bool
	for start := 0; start < len(header); {
		end := nextHeaderField(header, start)
		field := strings.ReplaceAll(header[start:end], "\x00", " ")
		start = end
		stays := !has8bit(field) || (sessionUTF8 && utf8.ValidString(field))
		name, _, named := strings.Cut(field, ":")
		// A name holding an 8-bit octet is encoded with its field, which then has no name.
		named = named && (stays || !has8bit(name))
		matched := false
		for i, change := range changes {
			if named && strings.EqualFold(strings.TrimSpace(name), change.name) {
				matched = true
				if !placed[i] {
					change.write(w)
					placed[i] = true
				}
			} else if field == "\r\n" && !placed[i] {
				change.write(w)
				placed[i] = true
			}
		}
		switch {
		case matched:
		case stays:
			_, _ = w.WriteString(field)
		default:
			sevenBitField(w, field)
		}
	}
	for i, change := range changes {
		if !placed[i] {
			change.write(w)
		}
	}
}

// sevenBitField rewrites one field holding an 8-bit octet. Content-Type and
// Content-Disposition keep their parameters, see ParameterField, because an
// encoded word may not appear in a parameter (RFC 2047 §5); anything else has its
// 8-bit text turned into encoded words. A line that is not a field at all is
// encoded whole, so its octets survive and a reader still finds no field in it.
func sevenBitField(w wireSink, field string) {
	text, eol := strings.CutSuffix(field, "\r\n")
	name, value, ok := strings.Cut(text, ":")
	switch {
	case !ok || has8bit(name):
		WriteEncodedWords(w, unfoldHeader(text))
	default:
		written := false
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "content-type", "content-disposition":
			if rewritten, ok := ParameterField(name, unfoldHeader(value)); ok {
				_, _ = w.WriteString(rewritten)
				written = true
			}
		}
		if !written {
			_, _ = w.WriteString(name)
			_, _ = w.WriteString(":")
			encodeHeaderValue(w, len(name)+1, value)
		}
	}
	if eol {
		_, _ = w.WriteString("\r\n")
	}
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
func encodeHeaderValue(w wireSink, column int, value string) {
	encodeHeaderTokens(w, column, &headerTokens{value: value, unclosed: len(value)})
}

// encodeHeaderTokens is encodeHeaderValue over tokens, whose steps then say what it scanned.
func encodeHeaderTokens(w wireSink, column int, tokens *headerTokens) {
	value := tokens.value
	line := column
	write := func(s string) {
		_, _ = w.WriteString(s)
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
		first := true
		eachEncodedWord(run, func(open string, content []byte) {
			if !first || line+len(open)+len(content)+len(wordClose)+len(trail) > 76 && line > 1 {
				write("\r\n ")
			}
			first = false
			_, _ = w.WriteString(open)
			_, _ = w.Write(content)
			_, _ = w.WriteString(wordClose)
			line += len(open) + len(content) + len(wordClose)
		})
		write(trail)
		i = end
	}
}

func isHeaderSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// headerTokens reads the tokens of one field value. unclosed is the offset of the
// first '"' found to have no closing quote, so that scan runs only once: every later
// '"' in the value has none either. A scan that reached a later '"' would have closed
// on it, so it must have skipped it as a quoted-pair, and a scan from just after that
// '"' follows the same octets to the end of the value. steps counts the octets looked
// at, which a scan repeating itself over an unclosed quote would multiply without
// allocating anything.
type headerTokens struct {
	value    string
	unclosed int
	steps    int
}

// at is the token starting at offset start: up to white space, a quoted-string that
// closes counting as part of it, spaces and all.
func (t *headerTokens) at(start int) string {
	i := start
	for i < len(t.value) && !isHeaderSpace(t.value[i]) {
		t.steps++
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
		t.steps++
		switch t.value[j] {
		case '\\':
			j++
		case '"':
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
	var b strings.Builder
	WriteEncodedWords(&b, text)
	return b.String()
}

// WriteEncodedWords is EncodeWords into w, holding one word at a time.
func WriteEncodedWords(w interface {
	io.Writer
	io.StringWriter
}, text string) {
	first := true
	eachEncodedWord(text, func(open string, content []byte) {
		if !first {
			_, _ = w.WriteString("\r\n ")
		}
		first = false
		_, _ = w.WriteString(open)
		_, _ = w.Write(content)
		_, _ = w.WriteString(wordClose)
	})
}

// wordCharset names what text is: UTF-8 when it is valid UTF-8. Anything else is
// UNKNOWN-8BIT (RFC 1428), so the octets survive and no charset is guessed.
func wordCharset(text string) string {
	if utf8.ValidString(text) {
		return "UTF-8"
	}
	return "UNKNOWN-8BIT"
}

const wordClose = "?="

// encodedWords spells text as encoded words of at most 75 characters (RFC 2047
// §2), never splitting a UTF-8 character across two (§5).
func encodedWords(text string) []string {
	var words []string
	eachEncodedWord(text, func(open string, content []byte) {
		words = append(words, open+string(content)+wordClose)
	})
	return words
}

// eachEncodedWord hands over each word of text as its opening and its content, in a
// buffer the next word reuses.
func eachEncodedWord(text string, emit func(open string, content []byte)) {
	open := "=?UNKNOWN-8BIT?Q?"
	charset := wordCharset(text)
	if charset == "UTF-8" {
		open = "=?UTF-8?Q?"
	}
	word := wordBuffers.Get().(*[75]byte)
	defer wordBuffers.Put(word)
	cur := word[:0]
	var encoded [3 * utf8.UTFMax]byte
	for i := 0; i < len(text); {
		size := 1
		if charset == "UTF-8" {
			_, size = utf8.DecodeRuneInString(text[i:])
		}
		enc := appendQEncoded(encoded[:0], text[i:i+size])
		if len(cur) > 0 && len(open)+len(cur)+len(enc)+len(wordClose) > 75 {
			emit(open, cur)
			cur = cur[:0]
		}
		cur = append(cur, enc...)
		i += size
	}
	emit(open, cur)
}

// wordBuffers keeps a message of many small encoded fields from allocating for each.
var wordBuffers = sync.Pool{New: func() any { return new([75]byte) }}

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
