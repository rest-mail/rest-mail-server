// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in Go's LICENSE file.

// Package mailaddr reads an RFC 5322 address list as net/mail reads it, holding nothing per address
// (TestParseMatchesNetMail): that package builds a list of pointers, a string or two for every address
// and an error that quotes the rest of the field, which comes to many times a long field's size.
//
// The parser is net/mail's, with its errors made values and each result a view of the field wherever
// the field spells it.
package mailaddr

import (
	"errors"
	"io"
	"mime"
	"net"
	"strings"
	"unicode/utf8"
)

// ErrNotAddressList is what a field that doesn't read as an address list gets.
var ErrNotAddressList = errors.New("mailaddr: not an address list")

// Unread says of a charset that nothing reads it. A word in one is text like any other, whose address
// is still read, and that takes no reader to find out. It may be nil.
type Unread func(charset string) bool

// WordCharset is the charset an encoded-word names, if word starts as one.
func WordCharset(word string) (charset string, ok bool) {
	rest, ok := strings.CutPrefix(word, "=?")
	if !ok {
		return "", false
	}
	charset, _, ok = strings.Cut(rest, "?")
	return charset, ok
}

// Valid reports whether the field reads as an address list, and how many addresses that gives. An
// encoded-word that dec can't read and unread doesn't name changes the answer, as it does for net/mail.
func Valid(list string, dec *mime.WordDecoder, unread Unread) (int, bool) {
	count := 0
	p := parser{s: list, count: &count, unread: unread}
	p.setDecoder(dec)
	if p.parseAddressList() != nil {
		return 0, false
	}
	return count, true
}

// EachAddress is Each with no name read, for a caller that wants only the addresses.
func EachAddress(list string, dec *mime.WordDecoder, unread Unread, visit func(address string)) error {
	p := parser{s: list, addresses: visit, unread: unread}
	p.setDecoder(dec)
	return p.parseAddressList()
}

// Each calls visit with the name and address of everything in the field, which has to be Valid: a
// field that goes wrong partway has been visited as far as that. What it gets may be views of list.
func Each(list string, dec *mime.WordDecoder, unread Unread, visit func(name, address string)) error {
	p := parser{s: list, visit: visit, unread: unread}
	p.setDecoder(dec)
	return p.parseAddressList()
}

type parser struct {
	s     string
	visit func(name, address string)
	// addresses is visit with the name left unread.
	addresses func(address string)
	count     *int
	// dec reads an encoded-word with the caller's CharsetReader, and sets charsetFailed when that
	// is what went wrong.
	dec           mime.WordDecoder
	charsetFailed bool
	unread        Unread
}

func (p *parser) setDecoder(dec *mime.WordDecoder) {
	reader := dec.CharsetReader
	p.dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if reader == nil {
			p.charsetFailed = true
			return nil, ErrNotAddressList
		}
		r, err := reader(charset, input)
		if err != nil {
			p.charsetFailed = true
		}
		return r, err
	}
}

func (p *parser) found(name, address string) {
	switch {
	case p.count != nil:
		*p.count++
	case p.addresses != nil:
		p.addresses(address)
	default:
		p.visit(name, address)
	}
}

// building says whether a name is wanted, or only whether there is one.
func (p *parser) building() bool { return p.visit != nil }

// buildingSpec says whether an address is wanted.
func (p *parser) buildingSpec() bool { return p.count == nil }

func (p *parser) parseAddressList() error {
	for {
		p.skipSpace()

		// allow skipping empty entries (RFC5322 obs-addr-list)
		if p.consume(',') {
			continue
		}

		if err := p.parseAddress(true); err != nil {
			return err
		}

		if !p.skipCFWS() {
			return ErrNotAddressList
		}
		if p.empty() {
			break
		}
		if p.peek() != ',' {
			return ErrNotAddressList
		}

		// Skip empty entries for obs-addr-list.
		for p.consume(',') {
			p.skipSpace()
		}
		if p.empty() {
			break
		}
	}
	return nil
}

// parseAddress parses a single RFC 5322 address at the start of p.
func (p *parser) parseAddress(handleGroup bool) error {
	p.skipSpace()
	if p.empty() {
		return ErrNotAddressList
	}

	// addr-spec has a more restricted grammar than name-addr,
	// so try parsing it first, and fallback to name-addr.
	spec, err := p.consumeAddrSpec()
	if err == nil {
		var displayName string
		p.skipSpace()
		if !p.empty() && p.peek() == '(' {
			displayName, err = p.consumeDisplayNameComment()
			if err != nil {
				return err
			}
		}
		p.found(displayName, spec)
		return nil
	}

	// display-name
	var displayName string
	if p.peek() != '<' {
		displayName, err = p.consumePhrase()
		if err != nil {
			return err
		}
	}

	p.skipSpace()
	if handleGroup {
		if p.consume(':') {
			return p.consumeGroupList()
		}
	}
	// angle-addr = "<" addr-spec ">"
	if !p.consume('<') {
		return ErrNotAddressList
	}
	spec, err = p.consumeAddrSpec()
	if err != nil {
		return err
	}
	if !p.consume('>') {
		return ErrNotAddressList
	}
	p.found(displayName, spec)
	return nil
}

func (p *parser) consumeGroupList() error {
	// handle empty group.
	p.skipSpace()
	if p.consume(';') {
		if !p.skipCFWS() {
			return ErrNotAddressList
		}
		return nil
	}

	for {
		p.skipSpace()
		// embedded groups not allowed.
		if err := p.parseAddress(false); err != nil {
			return err
		}

		if !p.skipCFWS() {
			return ErrNotAddressList
		}
		if p.consume(';') {
			if !p.skipCFWS() {
				return ErrNotAddressList
			}
			break
		}
		if !p.consume(',') {
			return ErrNotAddressList
		}
	}
	return nil
}

// consumeAddrSpec parses a single RFC 5322 addr-spec at the start of p.
func (p *parser) consumeAddrSpec() (spec string, err error) {
	orig := p.s
	defer func() {
		if err != nil {
			p.s = orig
		}
	}()

	// local-part = dot-atom / quoted-string
	var localPart string
	p.skipSpace()
	if p.empty() {
		return "", ErrNotAddressList
	}
	start := p.s
	quoted := p.peek() == '"'
	if quoted {
		// quoted-string
		localPart, err = p.consumeQuotedString()
		if err == nil && localPart == "" {
			err = ErrNotAddressList
		}
	} else {
		// dot-atom
		localPart, err = p.consumeAtom(false)
	}
	if err != nil {
		return "", err
	}

	if !p.consume('@') {
		return "", ErrNotAddressList
	}

	// domain = dot-atom / domain-literal
	var domain string
	p.skipSpace()
	if p.empty() {
		return "", ErrNotAddressList
	}

	if p.peek() == '[' {
		// domain-literal
		domain, err = p.consumeDomainLiteral()
	} else {
		// dot-atom
		domain, err = p.consumeAtom(false)
	}
	if err != nil {
		return "", err
	}

	if !p.buildingSpec() {
		return "", nil
	}
	if spelled := start[:len(start)-len(p.s)]; !quoted && len(spelled) == len(localPart)+1+len(domain) {
		return spelled, nil
	}
	return localPart + "@" + domain, nil
}

// consumePhrase parses the RFC 5322 phrase at the start of p.
func (p *parser) consumePhrase() (phrase string, err error) {
	// phrase = 1*word
	var (
		// Only a name of more than one word is built.
		first, joined strings.Builder
		view          string
		// pending is how much of a run of encoded-words hasn't gone in yet.
		pending, words int
	)
	add := func(word string) {
		if p.building() {
			switch words {
			case 0:
				view = word
			case 1:
				joined.WriteString(view)
				joined.WriteByte(' ')
				joined.WriteString(word)
			default:
				joined.WriteByte(' ')
				joined.WriteString(word)
			}
		}
		words++
	}
	flush := func() {
		add(first.String())
		first.Reset()
		pending = 0
	}
	for {
		// obs-phrase allows CFWS after one word
		if words > 0 {
			if !p.skipCFWS() {
				return "", ErrNotAddressList
			}
		}
		// word = atom / quoted-string
		var word string
		p.skipSpace()
		if p.empty() {
			break
		}
		isEncoded := false
		if p.peek() == '"' {
			// quoted-string
			word, err = p.consumeQuotedString()
		} else {
			// atom
			// We actually parse dot-atom here to be more permissive
			// than what RFC 5322 specifies.
			word, err = p.consumeAtom(true)
			if err == nil {
				word, isEncoded, err = p.decodeWord(word)
			}
		}

		if err != nil {
			break
		}
		switch {
		case isEncoded:
			pending += len(word)
			if p.building() {
				first.WriteString(word)
			}
		case pending > 0:
			flush()
			add(word)
		default:
			add(word)
		}
	}

	if pending > 0 {
		flush()
	}

	// Ignore any error if we got at least one word.
	if err != nil && words == 0 {
		return "", ErrNotAddressList
	}
	if words > 1 && p.building() {
		return joined.String(), nil
	}
	return view, nil
}

// consumeQuotedString parses the quoted string at the start of p.
func (p *parser) consumeQuotedString() (qs string, err error) {
	// Assume first byte is '"'.
	i := 1
	var unescaped *strings.Builder
	escaped := false

Loop:
	for {
		r, size := utf8.DecodeRuneInString(p.s[i:])

		switch {
		case size == 0:
			return "", ErrNotAddressList

		case size == 1 && r == utf8.RuneError:
			return "", ErrNotAddressList

		case escaped:
			//  quoted-pair = ("\" (VCHAR / WSP))

			if !isVchar(r) && !isWSP(r) {
				return "", ErrNotAddressList
			}
			if unescaped != nil {
				unescaped.WriteString(p.s[i : i+size])
			}
			escaped = false

		case isQtext(r) || isWSP(r):
			// qtext (printable US-ASCII excluding " and \), or
			// FWS (almost; we're ignoring CRLF)
			if unescaped != nil {
				unescaped.WriteString(p.s[i : i+size])
			}

		case r == '"':
			break Loop

		case r == '\\':
			if unescaped == nil {
				unescaped = new(strings.Builder)
				unescaped.WriteString(p.s[1:i])
			}
			escaped = true

		default:
			return "", ErrNotAddressList
		}

		i += size
	}
	if unescaped != nil {
		qs = unescaped.String()
	} else {
		qs = p.s[1:i]
	}
	p.s = p.s[i+1:]
	return qs, nil
}

// consumeAtom parses an RFC 5322 dot-atom at the start of p. If permissive is true, it will not fail
// on leading, trailing or double dots in the atom (see golang.org/issue/4938).
func (p *parser) consumeAtom(permissive bool) (atom string, err error) {
	i := 0

Loop:
	for {
		r, size := utf8.DecodeRuneInString(p.s[i:])
		switch {
		case size == 1 && r == utf8.RuneError:
			return "", ErrNotAddressList

		case size == 0 || !isAtext(r):
			break Loop

		default:
			i += size
		}
	}

	if i == 0 {
		return "", ErrNotAddressList
	}
	atom, p.s = p.s[:i], p.s[i:]
	if !permissive {
		if atom[0] == '.' || atom[len(atom)-1] == '.' || strings.Contains(atom, "..") {
			return "", ErrNotAddressList
		}
	}
	return atom, nil
}

// consumeDomainLiteral parses an RFC 5322 domain-literal at the start of p.
func (p *parser) consumeDomainLiteral() (string, error) {
	// Skip the leading [
	if !p.consume('[') {
		return "", ErrNotAddressList
	}

	// Parse the dtext
	dtext := p.s
	dtextLen := 0
	for {
		if p.empty() {
			return "", ErrNotAddressList
		}
		if p.peek() == ']' {
			break
		}

		r, size := utf8.DecodeRuneInString(p.s)
		if size == 1 && r == utf8.RuneError {
			return "", ErrNotAddressList
		}
		if !isDtext(r) {
			return "", ErrNotAddressList
		}

		dtextLen += size
		p.s = p.s[size:]
	}
	dtext = dtext[:dtextLen]

	// Skip the trailing ]
	if !p.consume(']') {
		return "", ErrNotAddressList
	}

	// Check if the domain literal is an IP address
	if net.ParseIP(dtext) == nil {
		return "", ErrNotAddressList
	}

	return "[" + dtext + "]", nil
}

func (p *parser) consumeDisplayNameComment() (string, error) {
	if !p.consume('(') {
		return "", ErrNotAddressList
	}
	start := p.s
	if !p.skipComment() {
		return "", ErrNotAddressList
	}
	spelled := start[:len(start)-len(p.s)-1]
	if !p.building() && !strings.Contains(spelled, "=?") && strings.IndexByte(spelled, '\\') < 0 {
		return "", nil
	}
	comment := commentText(spelled)

	// TODO(stapelberg): parse quoted-string within comment
	words := strings.FieldsFunc(comment, func(r rune) bool { return r == ' ' || r == '\t' })
	for idx, word := range words {
		decoded, isEncoded, err := p.decodeWord(word)
		if err != nil {
			return "", err
		}
		if isEncoded {
			words[idx] = decoded
		}
	}

	return strings.Join(words, " "), nil
}

// commentText is a comment less its closing parenthesis with each quoted-pair read, as net/mail's
// consumeComment gives it.
func commentText(spelled string) string {
	if strings.IndexByte(spelled, '\\') < 0 {
		return spelled
	}
	var b strings.Builder
	b.Grow(len(spelled))
	for i := 0; i < len(spelled); i++ {
		if spelled[i] == '\\' && i+1 < len(spelled) {
			i++
		}
		b.WriteByte(spelled[i])
	}
	return b.String()
}

func (p *parser) consume(c byte) bool {
	if p.empty() || p.peek() != c {
		return false
	}
	p.s = p.s[1:]
	return true
}

// skipSpace skips the leading space and tab characters.
func (p *parser) skipSpace() {
	for len(p.s) > 0 && (p.s[0] == ' ' || p.s[0] == '\t') {
		p.s = p.s[1:]
	}
}

func (p *parser) peek() byte { return p.s[0] }

func (p *parser) empty() bool { return len(p.s) == 0 }

// skipCFWS skips CFWS as defined in RFC5322.
func (p *parser) skipCFWS() bool {
	p.skipSpace()

	for {
		if !p.consume('(') {
			break
		}

		if !p.skipComment() {
			return false
		}

		p.skipSpace()
	}

	return true
}

// skipComment goes past a comment whose '(' is already consumed, and its closing ')'.
func (p *parser) skipComment() bool {
	depth := 1
	for {
		if p.empty() || depth == 0 {
			break
		}

		if p.peek() == '\\' && len(p.s) > 1 {
			p.s = p.s[1:]
		} else if p.peek() == '(' {
			depth++
		} else if p.peek() == ')' {
			depth--
		}
		p.s = p.s[1:]
	}
	return depth == 0
}

// decodeWord is net/mail's decodeRFC2047Word.
func (p *parser) decodeWord(s string) (word string, isEncoded bool, err error) {
	// mime.WordDecoder refuses anything that doesn't start so, and that isn't an encoded-word.
	if !strings.HasPrefix(s, "=?") {
		return s, false, nil
	}
	if p.unread != nil {
		if charset, ok := WordCharset(s); ok && p.unread(charset) {
			return s, false, nil
		}
	}
	if !p.building() {
		if decodes, empty, known := qWordDecodes(s); known {
			if !decodes {
				return s, false, nil
			}
			if empty {
				return "", true, nil
			}
			return s, true, nil
		}
	}
	p.charsetFailed = false
	word, err = p.dec.Decode(s)
	if err == nil {
		return word, true, nil
	}

	// If the error came from the character set reader
	// (meaning the character set itself is invalid
	// but the decoding worked fine until then),
	// return the original text and the error,
	// with isEncoded=true.
	if p.charsetFailed {
		return s, true, err
	}

	// Ignore invalid RFC 2047 encoded-word errors.
	return s, false, nil
}

// qWordDecodes answers, with nothing built, what mime.WordDecoder.Decode would of a Q-encoded word in
// a charset it reads by itself: whether it decodes, and whether to nothing. known is false for
// anything else, for Decode to say.
func qWordDecodes(s string) (decodes, empty, known bool) {
	if len(s) < 8 || !strings.HasSuffix(s, "?=") || strings.Count(s, "?") != 4 {
		return false, false, true
	}
	charset, rest, _ := strings.Cut(s[2:len(s)-2], "?")
	if !strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "iso-8859-1") && !strings.EqualFold(charset, "us-ascii") {
		return false, false, false
	}
	encoding, text, _ := strings.Cut(rest, "?")
	if encoding != "q" && encoding != "Q" {
		return false, false, false
	}
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '=':
			if i+2 >= len(text) || !isHex(text[i+1]) || !isHex(text[i+2]) {
				return false, false, true
			}
			i += 2
		case c == '_', (c <= '~' && c >= ' ') || c == '\n' || c == '\r' || c == '\t':
		default:
			return false, false, true
		}
	}
	return true, text == "", true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'F' || c >= 'a' && c <= 'f'
}

// isAtext reports whether r is an RFC 5322 atext character, or a period.
func isAtext(r rune) bool {
	switch r {
	case '.':
		return true

	// RFC 5322 3.2.3. specials
	case '(', ')', '<', '>', '[', ']', ':', ';', '@', '\\', ',', '"':
		return false
	}
	return isVchar(r)
}

// isQtext reports whether r is an RFC 5322 qtext character.
func isQtext(r rune) bool {
	// Printable US-ASCII, excluding backslash or quote.
	if r == '\\' || r == '"' {
		return false
	}
	return isVchar(r)
}

// isVchar reports whether r is an RFC 5322 VCHAR character.
func isVchar(r rune) bool {
	// Visible (printing) characters.
	return '!' <= r && r <= '~' || r >= utf8.RuneSelf
}

// isWSP reports whether r is a WSP (white space).
func isWSP(r rune) bool {
	return r == ' ' || r == '\t'
}

// isDtext reports whether r is an RFC 5322 dtext character.
func isDtext(r rune) bool {
	// Printable US-ASCII, excluding "[", "]", or "\".
	if r == '[' || r == ']' || r == '\\' {
		return false
	}
	return isVchar(r)
}
