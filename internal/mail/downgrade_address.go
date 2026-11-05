package mail

import (
	"io"
	"mime"
	"strings"
	"unsafe"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"

	"github.com/rest-mail/rest-mail-server/internal/mailaddr"
)

// downgradeAddressField makes an address field 7-bit by changing only what holds an
// 8-bit octet, in the order RFC 6857 section 3.2.1 gives. Everything else stays as it
// was sent, folding included.
//
//   - A comment or display name becomes encoded-words (sections 3.1.3, 3.1.5).
//   - An internationalized domain becomes its A-label (section 3.1.6).
//   - An address with no 7-bit form, because of its local part or a domain with no
//     A-label, becomes an encoded-word and " :;" after its display name (section
//     3.1.8). A group holding one becomes its display name, its whole group-list as
//     encoded-words and " :;", since a group can't nest (section 3.1.7).
//
// value is still folded, and column is where it starts on its line. ok is false when
// it doesn't read as an address list.
func downgradeAddressField(column int, value string) (string, bool) {
	var scan addressScan
	var b strings.Builder
	b.Grow(len(value))
	if !scan.field(&b, column, value) {
		return "", false
	}
	return b.String(), true
}

// field writes the downgraded value to out, or nothing and false when it doesn't read as a list. Only
// one mailbox's or group's tokens are held at a time, because a list of very many names would
// otherwise hold several times its size in tokens each time it's measured.
func (s *addressScan) field(out wordSink, column int, value string) bool {
	if !s.readsAsList(value, strings.LastIndexByte(value, ']')) {
		return false
	}
	s.write(out, column, value)
	return true
}

// write writes a value that reads as a list.
func (s *addressScan) write(out wordSink, column int, value string) {
	lastBracket := strings.LastIndexByte(value, ']')
	w := &addressWriter{out: out, line: column}
	var item []addressToken
	// next reads tokens onto item up to the next that's one of the specials, which it answers with.
	next := func(i int, specials string) (int, string) {
		for i < len(value) {
			kind, end := s.tokenAt(value, i, lastBracket)
			text := value[i:end]
			i = end
			if kind == tokenSpecial && strings.Contains(specials, text) {
				return i, text
			}
			item = append(item, addressToken{kind, text})
		}
		return i, ""
	}
	for i := 0; i < len(value); {
		item = item[:0]
		var special string
		i, special = next(i, ",:;")
		s.steps += len(item)
		if special == ":" {
			name := len(item)
			i, _ = next(i, ";")
			s.steps += len(item) - name
			w.group(item[:name], item[name:])
			continue
		}
		w.mailbox(item)
		if special != "" {
			w.write(special)
		}
	}
}

// passThroughCharset leaves what an encoded-word names to whatever reads the value later: it's only
// the shape of a list that's asked about.
var passThroughCharset = &mime.WordDecoder{
	CharsetReader: func(_ string, input io.Reader) (io.Reader, error) { return input, nil },
}

// readsAsList asks about the shape of a value, so that what fails to parse goes on being renamed. It
// can't read the value itself: it refuses octets that aren't UTF-8, a charset it has no reader for and
// a comment before a name or in an angle-addr, none of which changes the shape. Masking keeps every
// delimiter where it was. A group also has to close, for field to find its end.
func (s *addressScan) readsAsList(value string, lastBracket int) bool {
	masked := make([]byte, 0, len(value))
	open := false
	for i := 0; i < len(value); {
		kind, end := s.tokenAt(value, i, lastBracket)
		if end < 0 {
			return false
		}
		text := value[i:end]
		i = end
		switch {
		case kind == tokenComment:
			masked = append(masked, ' ')
			continue
		case kind == tokenAngle && strings.IndexByte(text, '(') >= 0:
			inner, _ := addressTokens(text[1 : len(text)-1])
			text = "<" + withoutComments(inner, "") + ">"
		case kind == tokenSpecial && text == ":":
			open = true
		case kind == tokenSpecial && text == ";":
			open = false
		}
		masked = appendMasked(masked, text)
	}
	if open {
		return false
	}
	_, ok := mailaddr.Valid(unsafe.String(unsafe.SliceData(masked), len(masked)), passThroughCharset)
	return ok
}

// appendMasked appends text unfolded, with an 'x' in place of each 8-bit octet.
func appendMasked(to []byte, text string) []byte {
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\n', c == '\r' && i+1 < len(text) && text[i+1] == '\n':
		case c >= 0x80:
			to = append(to, 'x')
		default:
			to = append(to, c)
		}
	}
	return to
}

// withoutComments joins the tokens with gap in place of each comment. A comment in an
// angle-addr leaves nothing, because net/mail allows no white space in an addr-spec.
func withoutComments(tokens []addressToken, gap string) string {
	var b strings.Builder
	for _, t := range tokens {
		switch t.kind {
		case tokenComment:
			b.WriteString(gap)
		case tokenAngle:
			inner, _ := addressTokens(t.text[1 : len(t.text)-1])
			b.WriteString("<" + withoutComments(inner, "") + ">")
		default:
			b.WriteString(t.text)
		}
	}
	return b.String()
}

type tokenKind int

const (
	tokenSpace tokenKind = iota
	tokenComment
	tokenQuoted
	tokenAngle
	tokenSpecial // ',', ':' or ';'
	tokenAtom
)

type addressToken struct {
	kind tokenKind
	text string
}

// isWord reports whether t is a word of a phrase or part of an addr-spec.
func (t addressToken) isWord() bool {
	return t.kind == tokenQuoted || t.kind == tokenAtom
}

func isFoldingSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// addressTokens splits a value into tokens; joining them gives the value back. '@' and
// '.' stay in their atom. ok is false when a quoted-string, comment or angle-addr never
// closes, or a comment closes that never opened.
func addressTokens(value string) (tokens []addressToken, ok bool) {
	var scan addressScan
	return scan.tokens(value)
}

// addressScan reads one address field. steps counts the octets and tokens it has looked
// at, so a test can hold a scan to the size of its input, which a clock can't do on a
// machine that's busy with something else.
type addressScan struct {
	steps int
}

func (s *addressScan) tokens(value string) (tokens []addressToken, ok bool) {
	lastBracket := strings.LastIndexByte(value, ']')
	for i := 0; i < len(value); {
		kind, end := s.tokenAt(value, i, lastBracket)
		if end < 0 {
			return nil, false
		}
		tokens = append(tokens, addressToken{kind, value[i:end]})
		i = end
	}
	return tokens, true
}

// tokenAt finds the token at value[i], and where it ends, or -1 if it never closes or
// closes what never opened. lastBracket is the offset of value's last ']', so a '[' with
// none after it doesn't scan for one.
func (s *addressScan) tokenAt(value string, i, lastBracket int) (tokenKind, int) {
	switch c := value[i]; {
	case isFoldingSpace(c):
		end := i + 1
		for end < len(value) && isFoldingSpace(value[end]) {
			s.steps++
			end++
		}
		return tokenSpace, end
	case c == '(':
		return tokenComment, s.commentEnd(value, i)
	case c == '"':
		return tokenQuoted, s.quotedEnd(value, i)
	case c == '<':
		return tokenAngle, s.angleEnd(value, i)
	case c == ',' || c == ':' || c == ';':
		s.steps++
		return tokenSpecial, i + 1
	case c == ')':
		return tokenAtom, -1
	}
	end := i
	for end < len(value) && !isFoldingSpace(value[end]) && strings.IndexByte(`()"<,:;`, value[end]) < 0 {
		if value[end] == '[' && end < lastBracket {
			// A domain literal holds colons.
			closing := strings.IndexByte(value[end:], ']')
			s.steps += closing
			end += closing
		}
		s.steps++
		end++
	}
	return tokenAtom, end
}

func (s *addressScan) angleEnd(value string, i int) int {
	for i++; i >= 0 && i < len(value); {
		s.steps++
		switch value[i] {
		case '>':
			return i + 1
		case '(':
			i = s.commentEnd(value, i)
		case '"':
			i = s.quotedEnd(value, i)
		default:
			i++
		}
	}
	return -1
}

func (s *addressScan) commentEnd(value string, i int) int {
	depth := 0
	for ; i < len(value); i++ {
		s.steps++
		switch value[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func (s *addressScan) quotedEnd(value string, i int) int {
	for i++; i < len(value); i++ {
		s.steps++
		switch value[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

func nextSpecial(tokens []addressToken, i int) int {
	for i < len(tokens) && tokens[i].kind != tokenSpecial {
		i++
	}
	return i
}

// addressWriter collects a converted value. It keeps an encoded-word apart from what
// is next to it (RFC 2047 section 5(3)), and starts a new line for one that would
// pass 76 characters (section 2).
type addressWriter struct {
	out       wordSink
	words     wordBytes
	line      int
	afterWord bool // the last thing written was an encoded-word
	lastSpace bool // the last thing written was white space
	endsWord  bool // the last thing written, space aside, shows as an encoded-word
}

func (w *addressWriter) write(s string) {
	if s == "" {
		return
	}
	if w.afterWord && !isFoldingSpace(s[0]) {
		w.raw(" ")
	}
	w.afterWord = false
	w.raw(s)
}

func (w *addressWriter) raw(s string) {
	_, _ = w.out.WriteString(s)
	if i := strings.LastIndex(s, "\r\n"); i >= 0 {
		w.line = len(s) - i - 2
	} else {
		w.line += len(s)
	}
	w.lastSpace = isFoldingSpace(s[len(s)-1])
}

// word writes text as encoded-words between open and closing, which stay outside.
func (w *addressWriter) word(open, text, closing string) {
	if w.afterWord || !w.lastSpace {
		w.raw(" ")
		w.afterWord = false
	}
	w.words = w.words[:0]
	wireform.WriteEncodedWords(&w.words, text)
	words := unsafe.String(unsafe.SliceData(w.words), len(w.words))
	first, _, _ := strings.Cut(words, "\r\n")
	if w.line+len(open)+len(first)+len(closing) > 76 && w.line > 1 {
		w.raw("\r\n ")
	}
	if open != "" {
		w.raw(open)
	}
	w.raw(words)
	if closing != "" {
		w.raw(closing)
	}
}

// wordBytes holds one run of encoded-words, again and again.
type wordBytes []byte

func (b *wordBytes) Write(p []byte) (int, error) {
	*b = append(*b, p...)
	return len(p), nil
}

func (b *wordBytes) WriteString(s string) (int, error) {
	*b = append(*b, s...)
	return len(s), nil
}

// comment writes a comment; one holding an 8-bit octet has its text encoded, and any
// comment nested in it goes into the word. Its delimiters stay outside (RFC 2047
// section 5(2)), so no space is needed around them.
func (w *addressWriter) comment(t addressToken) {
	w.endsWord = false
	if !containsNonASCII(t.text) {
		w.write(t.text)
		return
	}
	w.word("(", t.text[1:len(t.text)-1], ")")
}

// phrase writes a display name. Adjacent 8-bit words become one run of encoded-words,
// because the white space between encoded-words doesn't display (RFC 2047 section
// 6.2) and so has to be inside them. For the same reason a run next to an encoded-word
// that was already there takes the space between them in.
func (w *addressWriter) phrase(tokens []addressToken) {
	for i := 0; i < len(tokens); {
		t := tokens[i]
		switch t.kind {
		case tokenComment:
			w.comment(t)
			i++
			continue
		case tokenSpace:
			w.write(t.text)
			i++
			continue
		}
		end := wordEnd(tokens, i)
		spelled := joinTokens(tokens[i:end])
		if !containsNonASCII(spelled) {
			w.write(spelled)
			w.endsWord = isEncodedWord(spelled)
			i = end
			continue
		}
		var text strings.Builder
		text.WriteString(phraseText(tokens[i:end]))
		for {
			next := end
			for next < len(tokens) && tokens[next].kind == tokenSpace {
				next++
			}
			if next == end || next == len(tokens) || !tokens[next].isWord() {
				break
			}
			after := wordEnd(tokens, next)
			if !containsNonASCII(joinTokens(tokens[next:after])) {
				if isEncodedWord(joinTokens(tokens[next:after])) {
					text.WriteByte(' ')
				}
				break
			}
			text.WriteString(unfold(joinTokens(tokens[end:next])))
			text.WriteString(phraseText(tokens[next:after]))
			end = after
		}
		shown := text.String()
		if w.endsWord {
			shown = " " + shown
		}
		w.word("", shown, "")
		w.afterWord, w.endsWord = true, true
		i = end
	}
}

// wordEnd is where the word starting at tokens[i] ends: a quoted-string and an atom
// with nothing between them are one word.
func wordEnd(tokens []addressToken, i int) int {
	for i < len(tokens) && tokens[i].isWord() {
		i++
	}
	return i
}

func joinTokens(tokens []addressToken) string {
	if len(tokens) == 1 {
		return tokens[0].text
	}
	var b strings.Builder
	for _, t := range tokens {
		b.WriteString(t.text)
	}
	return b.String()
}

// phraseText is the text a word displays: a quoted-string loses its quotes and pairs.
func phraseText(tokens []addressToken) string {
	if len(tokens) == 1 {
		text := tokens[0].text
		if tokens[0].kind != tokenQuoted {
			return text
		}
		if strings.IndexByte(text, '\\') < 0 {
			return text[1 : len(text)-1]
		}
	}
	var b strings.Builder
	for _, t := range tokens {
		if t.kind != tokenQuoted {
			b.WriteString(t.text)
			continue
		}
		inner := t.text[1 : len(t.text)-1]
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				i++
			}
			b.WriteByte(inner[i])
		}
	}
	return b.String()
}

func isEncodedWord(s string) bool {
	return strings.HasPrefix(s, "=?") && strings.HasSuffix(s, "?=")
}

// group writes display-name ":" group-list ";", or section 3.1.7's empty group when a
// member has no 7-bit form.
func (w *addressWriter) group(name, members []addressToken) {
	w.phrase(name)
	for i := 0; i < len(members); {
		end := nextSpecial(members, i)
		if !hasSevenBitForm(members[i:end]) {
			w.noSevenBitForm(strings.TrimSpace(unfold(joinTokens(members))))
			return
		}
		i = end + 1
	}
	w.write(":")
	w.endsWord = false
	for i := 0; i < len(members); {
		end := nextSpecial(members, i)
		w.mailbox(members[i:end])
		if end < len(members) {
			w.write(members[end].text)
		}
		i = end + 1
	}
	w.write(";")
}

// noSevenBitForm writes text as the encoded-word and " :;" of sections 3.1.7 and 3.1.8,
// after the display name already written. Next to a name that shows as an encoded-word
// it starts with a space, which would not display otherwise.
func (w *addressWriter) noSevenBitForm(text string) {
	if w.endsWord {
		text = " " + text
	}
	w.word("", text, "")
	w.afterWord, w.endsWord = false, false
	w.write(" :;")
}

// mailbox writes one member of an address list: a display name and angle-addr, an
// addr-spec, or only white space and comments.
func (w *addressWriter) mailbox(item []addressToken) {
	defer func() { w.endsWord = false }()
	for i, t := range item {
		if t.kind == tokenAngle {
			w.phrase(item[:i])
			w.angleAddr(t)
			w.phrase(item[i+1:])
			return
		}
	}
	start, end := 0, len(item)
	for start < end && item[start].kind != tokenAtom && item[start].kind != tokenQuoted {
		start++
	}
	for end > start && item[end-1].kind != tokenAtom && item[end-1].kind != tokenQuoted {
		end--
	}
	w.phrase(item[:start])
	if spec := item[start:end]; len(spec) > 0 {
		if rewritten, ok := addrSpec(spec); ok {
			w.write(rewritten)
		} else {
			w.noSevenBitForm(addrSpecText(spec))
			w.commentsOf(spec)
		}
	}
	w.phrase(item[end:])
}

func (w *addressWriter) angleAddr(t addressToken) {
	if !containsNonASCII(t.text) {
		w.write(t.text)
		return
	}
	// The angle-addr closed, so what is inside it does too.
	spec, _ := addressTokens(t.text[1 : len(t.text)-1])
	if rewritten, ok := addrSpec(spec); ok {
		w.write("<" + rewritten + ">")
		return
	}
	w.noSevenBitForm(addrSpecText(spec))
	w.commentsOf(spec)
}

func (w *addressWriter) commentsOf(tokens []addressToken) {
	for _, t := range tokens {
		if t.kind == tokenComment {
			w.comment(t)
		}
	}
}

// addrSpecText is an addr-spec without its white space and comments.
func addrSpecText(tokens []addressToken) string {
	var b strings.Builder
	for _, t := range tokens {
		if t.isWord() {
			b.WriteString(t.text)
		}
	}
	return b.String()
}

// addrSpec spells an addr-spec in 7 bits, changing only a domain that holds an 8-bit
// octet to its A-label. ok is false when it has no 7-bit form.
func addrSpec(tokens []addressToken) (string, bool) {
	at := -1
	for i, t := range tokens {
		if t.kind == tokenAtom && strings.IndexByte(t.text, '@') >= 0 {
			at = i
		}
	}
	if at < 0 {
		return "", false
	}
	sign := strings.LastIndexByte(tokens[at].text, '@')
	before, after := tokens[:at], tokens[at+1:]
	if containsNonASCII(tokens[at].text[:sign]) || containsNonASCII(addrSpecText(before)) {
		return "", false
	}
	domain := tokens[at].text[sign+1:] + addrSpecText(after)
	var out strings.Builder
	for _, t := range before {
		out.WriteString(sevenBitToken(t))
	}
	out.WriteString(tokens[at].text[:sign+1])
	if !containsNonASCII(domain) {
		out.WriteString(tokens[at].text[sign+1:])
		for _, t := range after {
			out.WriteString(sevenBitToken(t))
		}
		return out.String(), true
	}
	ascii, err := DomainASCII(domain)
	if err != nil {
		return "", false
	}
	out.WriteString(ascii)
	for _, t := range after {
		if !t.isWord() {
			out.WriteString(sevenBitToken(t))
		}
	}
	return out.String(), true
}

// sevenBitToken spells a token of an address that has a 7-bit form. Only a comment
// can still hold an 8-bit octet.
func sevenBitToken(t addressToken) string {
	if t.kind == tokenComment && containsNonASCII(t.text) {
		return "(" + wireform.EncodeWords(t.text[1:len(t.text)-1]) + ")"
	}
	return t.text
}

// hasSevenBitForm reports whether a member of a group can stay an address.
func hasSevenBitForm(item []addressToken) bool {
	for _, t := range item {
		if t.kind == tokenAngle {
			if !containsNonASCII(t.text) {
				return true
			}
			spec, _ := addressTokens(t.text[1 : len(t.text)-1])
			_, ok := addrSpec(spec)
			return ok
		}
	}
	if !containsNonASCII(addrSpecText(item)) {
		return true
	}
	_, ok := addrSpec(item)
	return ok
}
