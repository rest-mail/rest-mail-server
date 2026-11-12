package message

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// addressParser reads address lists with every charset go-imap has a reader for,
// and leaves any other encoded word as it was sent, see keptWordReader.
var addressParser = mail.AddressParser{WordDecoder: &mime.WordDecoder{
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		if isLatin1(charset) {
			return latin1Reader(input)
		}
		return keptWordReader{}, nil
	},
}}

// keptWordReader fails when read. net/mail fails a whole address list when a
// CharsetReader returns an error, but treats a failed read like any other word it
// can't decode, and keeps the word's original text in the name. So the address
// stays, and the word still names its charset, with no byte guessed.
type keptWordReader struct{}

var errKeptWord = errors.New("charset has no reader")

func (keptWordReader) Read([]byte) (int, error) { return 0, errKeptWord }

// WriteTo fails as Read does. io.Copy uses it in place of the 32 KiB buffer it would otherwise take to
// read each word of a list that can be nothing but words.
func (keptWordReader) WriteTo(io.Writer) (int64, error) { return 0, errKeptWord }

// isKeptWord reports whether word is one addressParser kept: an encoded word in a
// charset neither it nor net/mail decodes.
func isKeptWord(word string) bool {
	body, ok := strings.CutPrefix(word, "=?")
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, "?=")
	if !ok || strings.Count(body, "?") != 2 || has8bit(body) {
		return false
	}
	charset, _, _ := strings.Cut(body, "?")
	return charset != "" && !decodableCharset(charset)
}

// addressNameText is metadataText for a personal or group name, which can hold both
// decoded text and words addressParser kept. A kept word is 7-bit and already names
// its charset, so only the text around it is encoded: encoding it again would have
// a client show its raw spelling.
func addressNameText(name string, sessionUTF8 bool) string {
	name = SpaceWordNUL(name)
	text := metadataText(name, sessionUTF8)
	if text == name {
		return name
	}
	var out, run []string
	flush := func() {
		if len(run) > 0 {
			out = append(out, metadataText(strings.Join(run, " "), sessionUTF8))
			run = nil
		}
	}
	for _, field := range strings.Split(name, " ") {
		if isKeptWord(field) {
			flush()
			out = append(out, field)
		} else {
			run = append(run, field)
		}
	}
	flush()
	return strings.Join(out, " ")
}

// BuildEnvelope constructs the ten ENVELOPE fields from the stored message's
// headers (RFC 3501 §7.4.2). Missing fields are NIL; only Sender and Reply-To
// default to From. INTERNALDATE and cached summary fields are not header values.
// The legacy Message fallback is retained only when no header block can be read.
// raw is the message as FETCH sends it, whose headers WireForm has already made
// 7-bit where the session requires it, so an 8-bit mailbox is left out without
// UTF-8: an encoded word may not stand in an addr-spec (RFC 2047 §5). sessionUTF8 is
// whether the session enabled UTF8=ACCEPT; see metadataText.
func BuildEnvelope(msg imap.Message, raw string, rawOK, sessionUTF8 bool) string {
	var hdr mail.Header
	if rawOK {
		// A malformed raw header block has no trustworthy envelope fields.
		hdr = mail.Header{}
		if m, err := mail.ReadMessage(strings.NewReader(raw)); err == nil {
			hdr = m.Header
		}
	}

	dateField := wire.QuoteString(msg.Date.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	subject := wire.QuoteString(metadataText(SpaceWordNUL(msg.Subject), sessionUTF8))
	fromFallback := buildAddress(msg.From.Name, msg.From.Email, sessionUTF8)
	toFallback := buildAddressList(msg.To, sessionUTF8)
	if hdr != nil {
		dateField = envelopeHeaderString(hdr, "Date")
		subject = envelopeSubject(hdr)
		fromFallback, toFallback = "NIL", "NIL"
	}
	from := headerAddressList(hdr, "From", fromFallback, sessionUTF8)
	sender := headerAddressList(hdr, "Sender", from, sessionUTF8)
	replyTo := headerAddressList(hdr, "Reply-To", from, sessionUTF8)
	to := headerAddressList(hdr, "To", toFallback, sessionUTF8)
	cc := headerAddressList(hdr, "Cc", "NIL", sessionUTF8)
	bcc := headerAddressList(hdr, "Bcc", "NIL", sessionUTF8)
	inReplyTo := envelopeHeaderString(hdr, "In-Reply-To")
	messageID := envelopeHeaderString(hdr, "Message-Id")

	return fmt.Sprintf("(%s %s %s %s %s %s %s %s %s %s)",
		dateField, subject, from, sender, replyTo, to, cc, bcc, inReplyTo, messageID)
}

// envelopeHeaderString distinguishes absent headers (NIL) from present empty
// headers (""). Reuse the astring encoder for quoted-specials and literal-only
// octets; neither Unicode nor a control character may escape into response lines.
func envelopeHeaderString(hdr mail.Header, name string) string {
	if _, present := hdr[name]; !present {
		return "NIL"
	}
	return wire.EncodeMailboxName(headerValue(hdr, name))
}

// envelopeSubject is the Subject as envelopeHeaderString has it, which a client decodes,
// so its encoded words follow SpaceWordNUL. Date, In-Reply-To and Message-Id are not
// text to decode, and stay as they are.
func envelopeSubject(hdr mail.Header) string {
	if _, present := hdr["Subject"]; !present {
		return "NIL"
	}
	return wire.EncodeMailboxName(SpaceWordNUL(headerValue(hdr, "Subject")))
}

// headerAddressList renders the named address-list header as an IMAP envelope
// address field, parsing it with the same buildAddressList that backs the To
// field. It returns fallback when the header is absent (or hdr is nil because the
// raw message was unavailable), and also when the header is present but parses to
// no address. Callers choose the fallback: normally NIL, or From for the Sender
// and Reply-To defaults.
func headerAddressList(hdr mail.Header, name, fallback string, sessionUTF8 bool) string {
	if hdr == nil {
		return fallback
	}
	v := strings.TrimSpace(hdr.Get(name))
	if v == "" {
		return fallback
	}
	if list := buildAddressList(v, sessionUTF8); list != "NIL" {
		return list
	}
	return fallback
}

// headerValue returns the trimmed value of the named header, or "" when it is
// absent or the raw message was unavailable (hdr is nil).
func headerValue(hdr mail.Header, name string) string {
	if hdr == nil {
		return ""
	}
	return strings.TrimSpace(hdr.Get(name))
}

// addressPart constructs a single IMAP address structure "(name NIL mailbox
// host)" — personal name, source-route, mailbox name and host name
// (RFC 3501 §7.4.2). It is the inner element of the parenthesized address lists
// that make up the from/sender/reply-to/to/cc/bcc envelope fields.
func addressPart(name, email string, sessionUTF8 bool, routes ...string) string {
	user, host := email, ""
	if position := strings.LastIndexByte(email, '@'); position >= 0 {
		user, host = email[:position], email[position+1:]
	}
	return mailboxPart(name, user, host, sessionUTF8, routes...)
}

// mailboxPart is addressPart for a mailbox whose local part and domain are
// already separated.
func mailboxPart(name, local, domain string, sessionUTF8 bool, routes ...string) string {
	route := ""
	if len(routes) != 0 {
		route = routes[0]
	}
	return fmt.Sprintf("(%s %s %s %s)", wire.QuoteString(addressNameText(name, sessionUTF8)), wire.QuoteString(route), wire.QuoteString(local), wire.QuoteString(domain))
}

// sourceRoutes holds the routes taken out of an address list, by the address
// each one leads to and in the order they came. Two uses of one address with
// different routes keep that order among themselves.
type sourceRoutes map[string][]string

// take returns the next route of email, or "" when it has none left.
func (r sourceRoutes) take(email string) string {
	routes := r[email]
	if len(routes) == 0 {
		return ""
	}
	r[email] = routes[1:]
	return routes[0]
}

// extractSourceRoutes removes the obs-route of each angle-addr in an RFC 5322
// address list, because net/mail refuses the whole list when one is present,
// and returns what is left with the routes it found. RFC 5322 §4.4 still has a
// reader accept a route, and RFC 3501 §9's addr-adl "Holds route from
// [RFC-2822] route-addr". Delimiters inside quoted strings, comments and domain
// literals are data, as in appendAddressGroups. Text that only starts like a
// route is left for net/mail to refuse. The scan is linear in the header's size,
// since a sender chooses the header: each attempt at a route stops at the next
// "<" that is not data, and everything else is passed over once.
func extractSourceRoutes(list string) (string, sourceRoutes) {
	routes := sourceRoutes{}
	if !strings.Contains(list, "<") || !strings.Contains(list, ":") {
		return list, routes
	}
	var kept strings.Builder
	comments := 0
	quoted, domain, escaped := false, false, false
	for i := 0; i < len(list); i++ {
		c := list[i]
		kept.WriteByte(c)
		switch {
		case escaped:
			escaped = false
		case c == '\\' && (quoted || comments > 0 || domain):
			escaped = true
		case comments > 0:
			switch c {
			case '(':
				comments++
			case ')':
				comments--
			}
		case quoted:
			quoted = c != '"'
		case domain:
			domain = c != ']'
		case c == '"':
			quoted = true
		case c == '(':
			comments = 1
		case c == '[':
			domain = true
		case c == '<':
			route, spec, end, ok := readSourceRoute(list[i+1:])
			if !ok {
				continue
			}
			if address, err := mail.ParseAddress("<" + spec + ">"); err == nil {
				routes[address.Address] = append(routes[address.Address], route)
				kept.WriteString(spec)
				i += end
			}
		}
	}
	return kept.String(), routes
}

// readSourceRoute reads obs-route and the addr-spec after it from text, which
// starts just after "<". It returns the route with its comments and folding
// white space removed, the addr-spec as written, and the offset of the ">" that
// closes them. ok is false when text does not start with a route.
func readSourceRoute(text string) (route, spec string, end int, ok bool) {
	comments := 0
	domain := false
	colon := -1
scan:
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case comments > 0:
			switch c {
			case '\\':
				i++
			case '(':
				comments++
			case ')':
				comments--
			}
		case domain:
			domain = c != ']'
		case c == '(':
			comments = 1
		case c == '[':
			domain = true
		case c == ':':
			colon = i
			break scan
		case c == '<' || c == '>' || c == '"':
			return "", "", 0, false
		}
	}
	if colon < 0 {
		return "", "", 0, false
	}
	var hops []string
	for _, hop := range strings.Split(stripComments(text[:colon]), ",") {
		if hop = strings.Join(strings.Fields(hop), ""); hop != "" {
			if hop[0] != '@' || len(hop) == 1 {
				return "", "", 0, false
			}
			hops = append(hops, hop)
		}
	}
	if len(hops) == 0 {
		return "", "", 0, false
	}
	closing := -1
	quoted := false
closeScan:
	for i := colon + 1; i < len(text); i++ {
		switch c := text[i]; {
		case quoted && c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case c == '>' && !quoted:
			closing = i
			break closeScan
		case c == '<' && !quoted:
			return "", "", 0, false
		}
	}
	if closing < 0 {
		return "", "", 0, false
	}
	return strings.Join(hops, ","), text[colon+1 : closing], closing, true
}

// buildAddress wraps a single address in the parenthesized address-list form an
// envelope field takes, e.g. ((name NIL mailbox host)). An empty address is NIL.
func buildAddress(name, email string, sessionUTF8 bool) string {
	if email == "" {
		return "NIL"
	}
	return "(" + addressPart(name, email, sessionUTF8) + ")"
}

// buildAddressList parses an RFC 5322 address-list header value (e.g.
// "Bob <bob@example.org>, carol@example.com") into the parenthesized list of
// address structures an envelope field requires. An empty value, a malformed
// group structure, or a list with no readable mailbox yields NIL, matching the
// RFC's treatment of an absent header. A mailbox that cannot be read is left
// out without losing the others in the list.
func buildAddressList(list string, sessionUTF8 bool) string {
	list = strings.TrimSpace(list)
	if list == "" {
		return "NIL"
	}
	list, routes := extractSourceRoutes(list)
	var b strings.Builder
	b.WriteByte('(')
	if !appendAddressGroups(&b, list, sessionUTF8, routes) || b.Len() == 1 {
		return "NIL"
	}
	b.WriteByte(')')
	return b.String()
}

// appendMailbox writes the address structure of one RFC 5322 mailbox. net/mail
// reads the current syntax and decodes display names; the obsolete forms RFC
// 5322 §4 has a reader accept, and RFC 5321 address literals that net/mail
// tests as bare IPs, are read by parseObsMailbox. A blank member (obs-mbox-list
// allows empty elements) writes nothing, as does a mailbox neither can read.
func appendMailbox(b *strings.Builder, value string, sessionUTF8 bool, routes sourceRoutes) {
	if strings.TrimSpace(stripComments(value)) == "" {
		return
	}
	if a, err := addressParser.Parse(value); err == nil {
		if !convertedAddrSpec(value, a.Address) {
			b.WriteString(addressPart(a.Name, a.Address, sessionUTF8, routes.take(a.Address)))
		}
		return
	}
	if name, local, domain, ok := parseObsMailbox(value); ok && !convertedAddrSpec(value, local+"@"+domain) {
		b.WriteString(mailboxPart(name, local, domain, sessionUTF8, routes.take(local+"@"+domain)))
	}
}

// convertedAddrSpec reports whether addrSpec holds a word WireForm converted from
// 8-bit text. Such a word may not stand there (RFC 2047 §5), so the mailbox has no
// 7-bit form and can't be read. WireForm encodes all of a token, '@' and '.' too, so
// a word it made has white space on any side where the addr-spec goes on. A word
// spelled against its neighbours in the field was sent that way and is legal atext.
// White space around a word that was sent that way reads the same, so that spelling
// is left out too.
func convertedAddrSpec(member, addrSpec string) bool {
	return holdsEncodedWord(addrSpec) && !strings.Contains(strings.ReplaceAll(member, `"`, ""), addrSpec)
}

// holdsEncodedWord reports whether a word of an addr-spec is spelled as an encoded word.
func holdsEncodedWord(addrSpec string) bool {
	for _, word := range strings.FieldsFunc(addrSpec, func(c rune) bool { return c == '.' || c == '@' }) {
		if strings.HasPrefix(word, "=?") && strings.HasSuffix(word, "?=") {
			return true
		}
	}
	return false
}

// appendAddressGroups writes the address structures of an address list,
// preserving RFC 3501 §7.4.2 group-start/group-end structures. Delimiters
// inside quoted strings, nested comments, angle addresses and domain literals
// are data. It reports false for a malformed group structure — a nested or
// unterminated group, a group end outside a group, or text after a group end —
// so no unmatched group sentinel is written.
func appendAddressGroups(b *strings.Builder, list string, sessionUTF8 bool, routes sourceRoutes) bool {
	inGroup, closed := false, false
	start, comments := 0, 0
	quoted, angle, domain, escaped := false, false, false, false
	for i := 0; i <= len(list); i++ {
		if i == len(list) {
			// The final member, after the last delimiter.
			if inGroup || (closed && strings.TrimSpace(stripComments(list[start:])) != "") {
				return false
			}
			appendMailbox(b, list[start:], sessionUTF8, routes)
			break
		}
		c := list[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && (quoted || comments > 0 || domain) {
			escaped = true
			continue
		}
		if comments > 0 {
			switch c {
			case '(':
				comments++
			case ')':
				comments--
			}
			continue
		}
		if quoted {
			if c == '"' {
				quoted = false
			}
			continue
		}
		if domain {
			if c == ']' {
				domain = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '(':
			comments++
		case '[':
			domain = true
		case '<':
			angle = true
		case '>':
			angle = false
		case ':', ',', ';':
			if angle {
				continue
			}
			member := list[start:i]
			if closed && strings.TrimSpace(stripComments(member)) != "" {
				return false
			}
			closed = false
			switch c {
			case ':':
				if inGroup {
					return false
				}
				// Parse the group's display-name as a mailbox display-name so
				// comments, quoted-pairs and encoded words share one decoder.
				name, err := addressParser.Parse(member + " <group@invalid>")
				if err != nil {
					return false
				}
				fmt.Fprintf(b, "(NIL NIL %s NIL)", wire.EncodeMailboxName(addressNameText(name.Name, sessionUTF8)))
				inGroup = true
			case ',':
				appendMailbox(b, member, sessionUTF8, routes)
			case ';':
				if !inGroup {
					return false
				}
				appendMailbox(b, member, sessionUTF8, routes)
				b.WriteString("(NIL NIL NIL NIL)")
				inGroup, closed = false, true
			}
			start = i + 1
		}
	}
	return true
}

// parseObsMailbox reads one mailbox — an addr-spec, or a display name and an
// angle address — with the obsolete syntax RFC 5322 §4 has a reader accept:
// a local part of dot-separated words where any word may be quoted (§4.4
// obs-local-part, e.g. "a".b), CFWS around the dots of the local part and the
// domain (§4.4, e.g. joe . q @ example . com), a period in the display name
// (§4.1 obs-phrase) and a source route before the addr-spec (§4.4 obs-route),
// which is skipped. A domain literal is any RFC 5322 dtext in brackets, kept
// as written, so an RFC 5321 address literal such as [IPv6:2001:db8::1] is
// read without an IP check. The local part is returned unquoted, as net/mail
// returns it.
func parseObsMailbox(value string) (name, local, domain string, ok bool) {
	toks, ok := addressTokens(value)
	if !ok {
		return "", "", "", false
	}
	spec := toks
	if lt := tokenIndex(toks, "<"); lt >= 0 {
		if last := toks[len(toks)-1]; !last.special || last.text != ">" || tokenIndex(toks[lt+1:], "<") >= 0 {
			return "", "", "", false
		}
		// obs-phrase: words and periods; a period keeps to the word before it.
		var phrase strings.Builder
		for i, t := range toks[:lt] {
			if t.literal || (t.special && t.text != ".") {
				return "", "", "", false
			}
			if i > 0 && t.text != "." {
				phrase.WriteByte(' ')
			}
			phrase.WriteString(t.text)
		}
		name = decodePhrase(phrase.String())
		spec = toks[lt+1 : len(toks)-1]
		// obs-route: "@" domain *("," ["@" domain]) ":" before the addr-spec.
		if colon := tokenIndex(spec, ":"); colon >= 0 && len(spec) > 0 && spec[0].text == "@" && spec[0].special {
			spec = spec[colon+1:]
		}
	}
	at := tokenIndex(spec, "@")
	if at <= 0 || at == len(spec)-1 {
		return "", "", "", false
	}
	local, ok = dottedWords(spec[:at], true)
	if !ok {
		return "", "", "", false
	}
	domainToks := spec[at+1:]
	if len(domainToks) == 1 && domainToks[0].literal {
		return name, local, domainToks[0].text, true
	}
	domain, ok = dottedWords(domainToks, false)
	return name, local, domain, ok
}

// addressToken is one lexical token of an RFC 5322 address: an atom, a
// quoted string (unquoted), a domain literal (as written) or a special.
type addressToken struct {
	text             string
	special, literal bool
	quoted           bool
}

// addressTokens splits an address into tokens, dropping CFWS. It reports false
// for an unterminated quoted string, comment or domain literal, or a control
// octet outside them.
func addressTokens(s string) ([]addressToken, bool) {
	var toks []addressToken
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '(':
			depth := 0
			for ; i < len(s); i++ {
				switch s[i] {
				case '\\':
					i++
				case '(':
					depth++
				case ')':
					depth--
				}
				if depth == 0 {
					break
				}
			}
			if depth != 0 {
				return nil, false
			}
			i++
		case c == '"':
			var b strings.Builder
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, false
			}
			i++
			toks = append(toks, addressToken{text: b.String(), quoted: true})
		case c == '[':
			end := i + 1
			for ; end < len(s) && s[end] != ']'; end++ {
				switch s[end] {
				case '\\':
					end++
				case '[':
					return nil, false
				}
			}
			if end >= len(s) {
				return nil, false
			}
			toks = append(toks, addressToken{text: s[i : end+1], literal: true})
			i = end + 1
		case strings.IndexByte("<>@.,:;\\]", c) >= 0:
			toks = append(toks, addressToken{text: s[i : i+1], special: true})
			i++
		case c < 0x20 || c == 0x7f:
			return nil, false
		default:
			end := i
			for end < len(s) && s[end] > 0x20 && s[end] != 0x7f && strings.IndexByte("()<>[]:;@\\,.\"", s[end]) < 0 {
				end++
			}
			toks = append(toks, addressToken{text: s[i:end]})
			i = end
		}
	}
	return toks, true
}

// tokenIndex returns the index of the first special token text, or -1.
func tokenIndex(toks []addressToken, text string) int {
	for i, t := range toks {
		if t.special && t.text == text {
			return i
		}
	}
	return -1
}

// dottedWords joins word *("." word) with periods. A word is an atom or, when
// quotedOK (a local part), a quoted string.
func dottedWords(toks []addressToken, quotedOK bool) (string, bool) {
	if len(toks)%2 == 0 {
		return "", false
	}
	var b strings.Builder
	for i, t := range toks {
		if i%2 == 1 {
			if !t.special || t.text != "." {
				return "", false
			}
			b.WriteByte('.')
			continue
		}
		if t.special || t.literal || (t.quoted && !quotedOK) || (!t.quoted && t.text == "") {
			return "", false
		}
		b.WriteString(t.text)
	}
	return b.String(), true
}

// decodePhrase decodes RFC 2047 encoded words in a display name with
// addressParser's charsets, keeping the text as written when it holds none or
// one of them cannot be decoded.
func decodePhrase(phrase string) string {
	if decoded, err := addressParser.WordDecoder.DecodeHeader(phrase); err == nil {
		return decoded
	}
	return phrase
}

// buildEncapsulatedEnvelope constructs the IMAP ENVELOPE (RFC 3501 §7.4.2) of a
// MESSAGE/RFC822 part's encapsulated message directly from its own headers — date,
// subject, from, sender, reply-to, to, cc, bcc, in-reply-to and message-id — with
// sender and reply-to defaulting to from. It differs from BuildEnvelope, which
// uses the stored Message model only when headers cannot be read; an
// encapsulated message has no such model, so every field comes from the raw header
// block. A header that cannot be parsed yields ten NIL fields, keeping the
// required envelope structure well-formed.
func buildEncapsulatedEnvelope(raw string, sessionUTF8 bool) string {
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return "(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)"
	}
	hdr := m.Header

	// Encapsulated envelopes follow the same header rules as FETCH ENVELOPE:
	// preserve the Date text, distinguish empty from absent scalar headers, and
	// frame literal-only octets rather than writing them in quoted strings.
	dateField := envelopeHeaderString(hdr, "Date")
	subject := envelopeSubject(hdr)
	from := headerAddressList(hdr, "From", "NIL", sessionUTF8)
	sender := headerAddressList(hdr, "Sender", from, sessionUTF8)
	replyTo := headerAddressList(hdr, "Reply-To", from, sessionUTF8)
	to := headerAddressList(hdr, "To", "NIL", sessionUTF8)
	cc := headerAddressList(hdr, "Cc", "NIL", sessionUTF8)
	bcc := headerAddressList(hdr, "Bcc", "NIL", sessionUTF8)
	inReplyTo := envelopeHeaderString(hdr, "In-Reply-To")
	messageID := envelopeHeaderString(hdr, "Message-Id")

	return fmt.Sprintf("(%s %s %s %s %s %s %s %s %s %s)",
		dateField, subject, from, sender, replyTo, to, cc, bcc, inReplyTo, messageID)
}
