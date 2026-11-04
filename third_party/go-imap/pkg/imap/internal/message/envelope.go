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

func (keptWordReader) Read([]byte) (int, error) {
	return 0, errors.New("charset has no reader")
}

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
// 7-bit where the session requires it, so an 8-bit mailbox reads NIL without UTF-8:
// an encoded word may not stand in an addr-spec (RFC 2047 §5). sessionUTF8 is
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
	subject := wire.QuoteString(metadataText(msg.Subject, sessionUTF8))
	fromFallback := buildAddress(msg.From.Name, msg.From.Email, sessionUTF8)
	toFallback := buildAddressList(msg.To, sessionUTF8)
	if hdr != nil {
		dateField = envelopeHeaderString(hdr, "Date")
		subject = envelopeHeaderString(hdr, "Subject")
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
// host)" — personal name, source-route (always NIL), mailbox name and host name
// (RFC 3501 §7.4.2). It is the inner element of the parenthesized address lists
// that make up the from/sender/reply-to/to/cc/bcc envelope fields. The name has
// been decoded, so it can hold 8-bit text; see addressNameText.
func addressPart(name, email string, sessionUTF8 bool) string {
	parts := strings.SplitN(email, "@", 2)
	user := parts[0]
	host := ""
	if len(parts) > 1 {
		host = parts[1]
	}
	return fmt.Sprintf("(%s NIL %s %s)", wire.QuoteString(addressNameText(name, sessionUTF8)), wire.QuoteString(user), wire.QuoteString(host))
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
// address structures an envelope field requires. An empty or unparseable value
// yields NIL, matching the RFC's treatment of an absent header.
func buildAddressList(list string, sessionUTF8 bool) string {
	list = strings.TrimSpace(list)
	if list == "" {
		return "NIL"
	}
	// net/mail validates the complete grammar but flattens groups, including
	// empty ones. Scan only structural delimiters here; keep address and phrase
	// decoding with net/mail instead of implementing a second address parser.
	addrs, err := addressParser.ParseList(list)
	if err != nil {
		return "NIL"
	}
	var b strings.Builder
	b.WriteByte('(')
	if !strings.Contains(list, ":") {
		for _, a := range addrs {
			b.WriteString(addressPart(a.Name, a.Address, sessionUTF8))
		}
	} else if !appendAddressGroups(&b, list, sessionUTF8) {
		return "NIL"
	}
	if b.Len() == 1 {
		return "NIL"
	}
	b.WriteByte(')')
	return b.String()
}

// appendAddressGroups preserves RFC 3501 §7.4.2 group-start/group-end
// structures. list has already passed addressParser. Delimiters inside
// quoted strings, nested comments, angle addresses and domain literals are data.
func appendAddressGroups(b *strings.Builder, list string, sessionUTF8 bool) bool {
	appendMembers := func(value string) bool {
		if strings.TrimSpace(stripComments(value)) == "" {
			return true
		}
		members, err := addressParser.ParseList(value)
		if err != nil {
			return false
		}
		for _, a := range members {
			b.WriteString(addressPart(a.Name, a.Address, sessionUTF8))
		}
		return true
	}
	start, comments := 0, 0
	quoted, angle, domain, escaped := false, false, false, false
	for i := 0; i < len(list); i++ {
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
			if c == ':' {
				// Parse the group's display-name as a mailbox display-name so
				// comments, quoted-pairs and encoded words share one decoder.
				name, err := addressParser.Parse(list[start:i] + " <group@invalid>")
				if err != nil {
					return false
				}
				fmt.Fprintf(b, "(NIL NIL %s NIL)", wire.EncodeMailboxName(addressNameText(name.Name, sessionUTF8)))
			} else {
				if !appendMembers(list[start:i]) {
					return false
				}
				if c == ';' {
					b.WriteString("(NIL NIL NIL NIL)")
				}
			}
			start = i + 1
		}
	}
	return appendMembers(list[start:])
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
	subject := envelopeHeaderString(hdr, "Subject")
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
